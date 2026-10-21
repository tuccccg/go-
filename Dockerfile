FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/agent-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agent-server /agent-server
EXPOSE 8080
ENTRYPOINT ["/agent-server"]
