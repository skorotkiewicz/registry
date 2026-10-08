FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod main.go web.go ./
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /registry . && mkdir /data

FROM scratch
COPY --from=build /registry /registry
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
ENV LISTEN_ADDR=0.0.0.0:8080 DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/registry"]
