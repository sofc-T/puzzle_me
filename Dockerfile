# container image builder docker file

FROM golang:1.24 AS builder

WORKDIR /app


COPY go.mod .

COPY go.sum .

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o puzzle_me main.go


FROM ubuntu:latest

WORKDIR /app

RUN useradd -s /bin/bash superappuser

RUN chown -R superappuser:superappuser /app
RUN apt-get update && apt-get install -y --no-install-recommends ncurses-bin && rm -rf /var/lib/apt/lists/*

COPY .env .env

USER superappuser


COPY --from=builder /app/puzzle_me puzzle_me 

COPY corebanking.json .

EXPOSE 8080

EXPOSE 50051

CMD ["./puzzle_me"]
