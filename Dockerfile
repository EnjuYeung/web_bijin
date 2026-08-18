FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bijin .

FROM alpine:3.21
RUN apk add --no-cache tzdata wget
WORKDIR /app
COPY --from=build /out/bijin /app/bijin
ENV PHOTOS_DIR=/photos DATA_DIR=/data LISTEN=:5001 TZ=Asia/Shanghai
EXPOSE 5001
ENTRYPOINT ["/app/bijin"]
