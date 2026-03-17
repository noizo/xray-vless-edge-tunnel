FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETARCH
WORKDIR /app
COPY cmd/admin/go.mod cmd/admin/go.sum ./
RUN go mod download
COPY cmd/admin/ .
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /xray-admin .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /xray-admin /xray-admin
USER nonroot:nonroot
ENTRYPOINT ["/xray-admin"]
