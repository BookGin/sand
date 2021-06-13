# Sand

![](https://github.com/bookgin/sand/actions/workflows/test.yml/badge.svg)

Sand is a minimal self-hosted file-sharing service, like Firefox Send but **without** encryption.

- Go 1.15+
- Redis 6.0+

## Getting Started

```
docker-compose up

# Access http://127.0.0.1:8080/upload
```

See `docker-compose.yml` for the configuration of environment variables. Also see `./redis.conf` for a example Redis configuration.

## Related works

- [mozilla/send](https://github.com/mozilla/send)
- [timvisee/ffsend](https://github.com/timvisee/ffsend)
- [Forceu/Gokapi](https://github.com/Forceu/Gokapi)
