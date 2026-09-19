FROM golang:1.27.1-bookworm@sha256:6ed48491acfb40533f6970d9d8ce3cbfc6f2cc7d81413c2f01c871c927d98634 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/watchmesh ./cmd/watchmesh
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/watchmesh /watchmesh
USER nonroot:nonroot
ENTRYPOINT ["/watchmesh"]
