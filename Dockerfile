FROM golang:latest as build

LABEL maintainer="bookgin"

RUN git clone https://github.com/bookgin/sand /root/sand
WORKDIR /root/sand
RUN go build

FROM ubuntu:20.04 as release
RUN useradd --home-dir /home/user --create-home user
WORKDIR /home/user
USER user

COPY --from=build /root/sand/sand ./sand
COPY ./public/ ./public
RUN mkdir ./upload
CMD ["./sand"]
