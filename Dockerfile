FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /docsbot ./cmd/docsbot

FROM gcr.io/distroless/static-debian12
COPY --from=build /docsbot /docsbot
ENTRYPOINT ["/docsbot"]