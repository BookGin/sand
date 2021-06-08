package main

import (
    "mime/multipart"
    "path/filepath"
    "time"
    "os"
    "errors"
    "fmt"
    "io"
    "context"
    "bytes"
    "strconv"
    "encoding/gob"
    "github.com/go-redis/redis/v8"
    "github.com/gin-gonic/gin"
)

var UPLOAD_DIR = getenv("UPLOAD_DIR", "./upload")
var LISTEN_HOST = getenv("LISTEN_HOST", "127.0.0.1:8080")
var REDIS_HOST = getenv("REDIS_HOST", "127.0.0.1:6379")
var REDIS_PASSWORD = getenv("REDIS_PASSWORD", "")
var REDIS_DB = getenv("REDIS_DB", "0")

const MAX_UPLOAD_SIZE = 2 << 20 // 50 MiB
var ctx = context.Background()
var rdb *redis.Client

type FileInfo struct {
    Name string
    RawFilename string
    UploadTimeStamp int64
    Lifespan int64
    DownloadQuota int64
    Size int64
}

func (info *FileInfo) Marshal() (string, error) {
    buf := bytes.Buffer{}
    err := gob.NewEncoder(&buf).Encode(info)
    return buf.String(), err
}

func UnmarshalToFileInfo(str string) (FileInfo, error) {
    decoder := gob.NewDecoder(bytes.NewBufferString(str))
    info := FileInfo{}
    err := decoder.Decode(&info)
    return info, err
}

type UploadForm struct {
    Name string `form:"name" binding:"required"`
    RawFile *multipart.FileHeader `form:"file" binding:"required"`
    Lifespan int64 `form:"life,default=-1" binding:"-"`
    DownloadQuota int64 `form:"quota,default=1" binding:"-"`
}

// https://github.com/gin-gonic/gin/blob/3f5c0518286b50108bc123eaf061ef80141dc701/context.go#L576-L592
func SaveUploadedFile(file *multipart.FileHeader, dst string) (int64, error) {
	if file.Size > MAX_UPLOAD_SIZE {
		return 0, errors.New(fmt.Sprintf("File too large (> %d MB)", MAX_UPLOAD_SIZE >> 20))
	}
	src, err := file.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	written, err := io.Copy(out, src)
	return written, err
}

func isSafeName(name *string) bool{
    for _, c := range *name {
        if !(c == '.' || c == '_' || c == '-' ||
            ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
            return false
        }
    }
    return true
}

func makeAbortResponse(c *gin.Context, statusCode int, status string, message string) {
    c.AbortWithStatusJSON(statusCode, gin.H{
        "status": status,
        "message": message,
    })
}

func ErrorResponse(c *gin.Context, message string) {
    makeAbortResponse(c, 500, "error", message)
}

func FailResponse(c *gin.Context, message string) {
    makeAbortResponse(c, 404, "fail", message)
}

func SuccessResponse(c *gin.Context, data *gin.H) {
    c.JSON(200, gin.H{
        "status": "success",
        "data": *data,
    })
}

func FetchFileInfo(c *gin.Context) {
	value, err := rdb.Get(ctx, c.Param("name")).Result()
	if err != nil {
            FailResponse(c, "Expired or quota exceeded!")
            return
	}
        info, err := UnmarshalToFileInfo(value)
        if err != nil {
            ErrorResponse(c, "Fail to unmarshal the data" + err.Error())
            return
	}
	c.Set("FileInfo", info)
	c.Next()
}

func DeleteFileFromDisk(name string) {
    err := os.Remove(filepath.Join(UPLOAD_DIR, name))
    if err == nil {
		fmt.Println("Delete file " + name +" from disk")
    } else {
		fmt.Println("Fail to delete the file " + name + " because " + err.Error())
    }
}

// https://stackoverflow.com/a/45978733
func getenv(key string, fallback string) string {
    value, exists := os.LookupEnv(key)
    if !exists {
        value = fallback
    }
    return value
}


func setupServer() *gin.Engine {
        r := gin.Default()
	gin.SetMode(getenv("GIN_MODE", "debug"))
        r.NoRoute(func(c *gin.Context) {
               FailResponse(c, "Not found")
        })
        r.StaticFile("/upload", "./public/upload.html")
        r.StaticFile("/", "./public/download.html")

        r.GET("/info/:name", FetchFileInfo, func(c *gin.Context) {
                info := c.MustGet("FileInfo").(FileInfo)
                SuccessResponse(c, &gin.H{
		    "Name": info.Name,
                    "RawFilename": info.RawFilename,
                    "UploadTimeStamp": info.UploadTimeStamp,
                    "Lifespan": info.Lifespan,
                    "DownloadQuota": info.DownloadQuota,
                    "Size": info.Size,
		})
        })
        // append ?dl=1 prevent bots
        r.GET("/download/:name", FetchFileInfo, func(c *gin.Context) {
                info := c.MustGet("FileInfo").(FileInfo)
                if info.DownloadQuota <= 0 || !(info.Lifespan == -1 || time.Now().Unix() < info.UploadTimeStamp + info.Lifespan)  {
                     FailResponse(c, "Expired or quota exceeded!")
                     return
                }
                info.DownloadQuota--
                if info.DownloadQuota <= 0 {
                    // even if the key does not exist (e.g. expired), the error is still nil
                    if err := rdb.Del(ctx, info.Name).Err(); err != nil {
                       ErrorResponse(c, "Delete key failed:" + err.Error())
                       return
                    }
                    defer DeleteFileFromDisk(info.Name)
                } else {
                    encodedStr, err := info.Marshal()
                    if err != nil {
		       ErrorResponse(c, "Marshal failed: " + err.Error())
                       return
                    }
                    if err := rdb.Set(ctx, info.Name, encodedStr, redis.KeepTTL).Err(); err != nil {
                       ErrorResponse(c, "Update failed:" + err.Error())
                       return
                    }
                }
                c.FileAttachment(filepath.Join(UPLOAD_DIR, info.Name), info.RawFilename)
        })
	r.GET("/healthcheck", func(c *gin.Context) {
		redis := "ok"
		if _, err := rdb.Ping(ctx).Result(); err != nil {
			redis = err.Error()
		}
                SuccessResponse(c, &gin.H{
			"redis": redis,
		})
	})
        r.POST("/upload", func(c *gin.Context) {
		form := UploadForm{}
                now := time.Now()
                if err := c.ShouldBind(&form); err != nil {
		    FailResponse(c, "Bad request: " + err.Error())
		    return
		}
		if !isSafeName(&form.Name) {
		    FailResponse(c, "Illegal name: " + form.Name)
		    return
		}
		if !(form.Lifespan == -1 || form.Lifespan >= 1) {
		    FailResponse(c, "The expiration must be later than now")
		    return
		}
                switch _, err := rdb.Get(ctx, form.Name).Result(); err {
	            case nil:
                        FailResponse(c, "The key already exists!")
                        return
                    case redis.Nil:
                        break
                    default:
                        ErrorResponse(c, "Fail to look up this key: " + err.Error())
                        return
                }
                written, err := SaveUploadedFile(form.RawFile, filepath.Join(UPLOAD_DIR, form.Name))
                if err != nil {
                        ErrorResponse(c, "Saving uploaded file failed:" + err.Error())
		        return
		}
                info := FileInfo {
                        Name: form.Name,
                        RawFilename: form.RawFile.Filename,
                        UploadTimeStamp: now.Unix(),
                        Lifespan: form.Lifespan,
                        DownloadQuota: form.DownloadQuota,
                        Size: written,
                }
                encodedStr, err := info.Marshal()
                if err != nil {
		        ErrorResponse(c, "Marshal failed: " + err.Error())
                        return
                }
                var expirationNs time.Duration
                if info.Lifespan == -1 {
                    expirationNs = 0 // won't be expired
                } else {
                    expirationNs = time.Unix(info.UploadTimeStamp + info.Lifespan, 0).Sub(now)
                }
                set, err := rdb.SetNX(ctx, info.Name, encodedStr, expirationNs).Result()
                if err != nil {
                        ErrorResponse(c, "Update failed:" + err.Error())
		        return
                }
                if set == false {
                        ErrorResponse(c, "Update failed (race condition):" + err.Error())
		        return
                }
		SuccessResponse(c, &gin.H{
		        "Name": info.Name,
                        "RawFilename": info.RawFilename,
                        "UploadTimeStamp": info.UploadTimeStamp,
                        "Lifespan": info.Lifespan,
                        "DownloadQuota": info.DownloadQuota,
                        "Size": written,
	        })
        })
        return r
}

func GoRoutineDeleteSubscriber() {
        // thr webserver will always be running so we don't care about closing the channel
        channel := rdb.Subscribe(ctx, "__keyevent@" + REDIS_DB + "__:expired").Channel()
        for {
		    msg := <-channel
                    DeleteFileFromDisk(msg.Payload)
        }
}

func setupRedis() {
	redisDb, err := strconv.Atoi(REDIS_DB)
	if err != nil {
		panic("Fail to parse redis db name")
	}
	rdb = redis.NewClient(&redis.Options{
	    Addr:     REDIS_HOST,
	    Password: REDIS_PASSWORD,
	    DB:       redisDb,
	})
}

func main() {
	setupRedis()
        go GoRoutineDeleteSubscriber()
        r := setupServer()
	r.Run(LISTEN_HOST)
}

