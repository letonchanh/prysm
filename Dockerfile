FROM golang:1.22 AS builder

WORKDIR /prysm
COPY go.mod go.sum ./
RUN export GOPROXY=direct
RUN go mod download

COPY . .
RUN go build -o /usr/local/bin/beacon-chain ./cmd/beacon-chain
# RUN go build -o /usr/local/bin/validator ./cmd/validator

ENTRYPOINT [ "beacon-chain" ]