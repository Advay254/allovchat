FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /allovchat .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D app
COPY --from=build /allovchat /allovchat
USER app
ENV PORT=8080
EXPOSE 8080
CMD ["/allovchat"]
