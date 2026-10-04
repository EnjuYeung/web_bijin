FROM node:24-alpine AS frontend
WORKDIR /src/frontend
ENV NEXT_TELEMETRY_DISABLED=1
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
COPY tokens.css /src/tokens.css
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bijin .

FROM alpine:3.22
RUN apk add --no-cache tzdata wget
WORKDIR /app
COPY --from=build /out/bijin /app/bijin
ENV PHOTOS_DIR=/photos DATA_DIR=/data LISTEN=:5001 TZ=Asia/Shanghai
EXPOSE 5001 5002
ENTRYPOINT ["/app/bijin"]
