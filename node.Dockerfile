FROM golang:1.23.5

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go install ./cmd/stor-node

ENTRYPOINT ["stor-node"]
