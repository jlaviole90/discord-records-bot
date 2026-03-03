ARG GO_VERSION=1.23.5

FROM golang:${GO_VERSION} AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -v -o /bin/discord-records-bot .

FROM alpine:3.20

RUN apk --no-cache add ca-certificates

COPY --from=build /bin/discord-records-bot /usr/local/bin/discord-records-bot

CMD ["discord-records-bot"]
