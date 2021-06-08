package main

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type Json = map[string]interface{}

var r *gin.Engine
var quotaName, expireName, notExistName string

func Test(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	quotaName, expireName, notExistName = randStr(), randStr(), randStr()

	setupRedis()
	go GoRoutineDeleteSubscriber()
	r = setupServer()

	t.Run("General", func(t *testing.T) {
		t.Run("Web and Redis connection", testRedis)
		t.Run("Invalid upload name", testInvalidName)
	})

	t.Run("Not-existing file "+notExistName, func(t *testing.T) {
		t.Run("Download a not existing file", newFailedDownloadTest(notExistName))
		t.Run("Info a not existing file", newFailedInfoTest(notExistName))
	})

	t.Run("A immediately expired file "+expireName, func(t *testing.T) {
		t.Run("Upload a valid lifespan 1 sec file", testUploadExpire)
		t.Run("Info a expired file", newFailedInfoTest(expireName))
		t.Run("Download a expired file", newFailedDownloadTest(expireName))
	})

	t.Run("A quota 1 file "+quotaName, func(t *testing.T) {
		t.Run("Upload a valid quota 1 file", testUploadQuota)
		t.Run("Upload a duplicated file", testUploadDuplicated)
		t.Run("Info a existing file", testInfoQuota)
		t.Run("Download a existing file", testDownloadQuota)
		t.Run("Download a no quota file", newFailedDownloadTest(quotaName))
		t.Run("Info a no quota file", newFailedInfoTest(quotaName))
	})

}

// helper

func assertTrue(t *testing.T, condition bool, message string, js Json) {
	if !condition {
		t.Error(jsonStringify(js))
		t.Fatal(message)
	}
}

func newRequest(method string, url string, form Json) (*http.Request, error) {
	writer, data := createPostData(form)
	req, err := http.NewRequest(method, url, data)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, err
}

func createPostData(form Json) (*multipart.Writer, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	writer := multipart.NewWriter(buf)
	for k, v := range form {
		if k == "file" {
			tuple := v.([]string)
			w, _ := writer.CreateFormFile(k, tuple[0])
			w.Write([]byte(tuple[1]))
		} else {
			w, _ := writer.CreateFormField(k)
			w.Write([]byte(v.(string)))
		}
	}
	writer.Close()
	return writer, buf
}

func httpDo(t *testing.T, method string, url string, body Json) (int, Json) {
	req, err := newRequest(method, url, body)
	if err != nil {
		t.Error("Fail to construct request: " + err.Error())
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	result := Json{}
	decoder := json.NewDecoder(res.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Error("Fail to decode as JSON: " + res.Body.String())
	}
	return res.Code, result
}

func jsonStringify(js Json) string {
	buf, err := json.MarshalIndent(js, "", "    ")
	if err != nil {
		panic("Fail to decode as Json")
	}
	return string(buf)
}

func randStr() string {
	return strconv.FormatInt(rand.Int63(), 36)
}

// Tests

func testRedis(t *testing.T) {
	status, res := httpDo(t, "GET", "/healthcheck", nil)
	assertTrue(t, status == http.StatusOK, "Non-200 response", res)
	assertTrue(t, res["status"].(string) == "success", "JSON response returns non-success", res)
	data := res["data"].(Json)
	assertTrue(t, data["redis"].(string) == "ok", "redis does not work properly", res)
}

func testInvalidName(t *testing.T) {
	status, res := httpDo(t, "POST", "/upload", Json{
		"name":  "invalid_name_because_it_contains_@",
		"quota": "42",
		"life":  strconv.FormatInt(3600, 10),
		"file":  []string{"file_name", "file_content"},
	})
	assertTrue(t, status == http.StatusNotFound, "Non-404 response", res)
	assertTrue(t, res["status"].(string) == "fail", "JSON response returns non-fail", res)
}

func testUploadQuota(t *testing.T) {
	status, res := httpDo(t, "POST", "/upload", Json{
		"name":  quotaName,
		"quota": "1",
		"life":  "3600",
		"file":  []string{"file_name", "file_content"},
	})
	assertTrue(t, status == http.StatusOK, "Non-200 response", res)
	assertTrue(t, res["status"].(string) == "success", "JSON response returns non-success", res)
	data := res["data"].(Json)
	assertTrue(t, data["Name"].(string) == quotaName, "Uploaded name is different", res)
	assertTrue(t, data["DownloadQuota"].(json.Number).String() == "1", "Uploaded quota is different", res)
	assertTrue(t, data["Lifespan"].(json.Number).String() == "3600", "Uploaded lifspan is different", res)
}

func testUploadDuplicated(t *testing.T) {
	status, res := httpDo(t, "POST", "/upload", Json{
		"name":  quotaName,
		"quota": "1",
		"life":  strconv.FormatInt(3600, 10),
		"file":  []string{"file_name", "file_content"},
	})
	assertTrue(t, status == http.StatusNotFound, "Non-404 response", res)
	assertTrue(t, res["status"].(string) == "fail", "JSON response returns non-fail", res)
}

func testUploadExpire(t *testing.T) {
	status, res := httpDo(t, "POST", "/upload", Json{
		"name":  expireName,
		"quota": "100",
		"life":  "1",
		"file":  []string{"file_name", "file_content"},
	})
	assertTrue(t, status == http.StatusOK, "Non-200 response", res)
	assertTrue(t, res["status"].(string) == "success", "JSON response returns non-success", res)
	data := res["data"].(Json)
	assertTrue(t, data["Name"].(string) == expireName, "Uploaded name is different", res)
	assertTrue(t, data["DownloadQuota"].(json.Number).String() == "100", "Uploaded quota is different", res)
	assertTrue(t, data["Lifespan"].(json.Number).String() == "1", "Uploaded lifspan is different", res)
	time.Sleep(1000 * time.Millisecond)
}

func newFailedInfoTest(name string) func(*testing.T) {
	return func(t *testing.T) {
		status, res := httpDo(t, "GET", "/info/"+name, nil)
		assertTrue(t, status == http.StatusNotFound, "Non-404 response", res)
		assertTrue(t, res["status"].(string) == "fail", "JSON response returns non-fail", res)
	}
}

func testInfoQuota(t *testing.T) {
	status, res := httpDo(t, "GET", "/info/"+quotaName, nil)
	assertTrue(t, status == http.StatusOK, "Non-200 response", res)
	assertTrue(t, res["status"].(string) == "success", "JSON response returns non-success", res)
	data := res["data"].(Json)
	assertTrue(t, data["Name"].(string) == quotaName, "Uploaded name is different", res)
	assertTrue(t, data["DownloadQuota"].(json.Number).String() == "1", "Uploaded quota is different", res)
	assertTrue(t, data["Lifespan"].(json.Number).String() == "3600", "Uploaded lifspan is different", res)
}

func newFailedDownloadTest(name string) func(*testing.T) {
	return func(t *testing.T) {
		status, res := httpDo(t, "GET", "/download/"+name, nil)
		assertTrue(t, status == http.StatusNotFound, "Non-404 response", res)
		assertTrue(t, res["status"].(string) == "fail", "JSON response returns non-fail", res)
	}
}

func testDownloadQuota(t *testing.T) {
	req, err := http.NewRequest("GET", "/download/"+quotaName, nil)
	if err != nil {
		t.Error("Fail to construct request: " + err.Error())
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	assertTrue(t, res.Code == http.StatusOK, "Non-200 response", nil)
	assertTrue(t, res.Body.String() == "file_content", "Downloaded file content is different: "+res.Body.String(), nil)
}
