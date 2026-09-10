# Custom Fabric-X kv-write view service.
#   docker build -t bench/fabricx-rest:latest deploy/docker/fabricx/kvview
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /kvview .

FROM gcr.io/distroless/static-debian12
COPY --from=build /kvview /kvview
EXPOSE 9700
ENTRYPOINT ["/kvview"]
