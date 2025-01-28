FROM golang:1.23.5

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go install ./cmd/stor-front

HEALTHCHECK --interval=1s CMD curl --fail http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["stor-front"]
