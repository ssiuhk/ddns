FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /ddns .

FROM alpine:3.24

COPY --from=build /ddns /usr/local/bin/ddns
USER nobody

ENV GIN_MODE=release
ENV DDNS_EXPIRATION_DAYS=10
ENV DDNS_DNSSEC=true

CMD ddns \
    --domain=${DDNS_DOMAIN} \
    --soa_fqdn=${DDNS_SOA_DOMAIN} \
    --redis=${DDNS_REDIS_HOST} \
    --expiration-days=${DDNS_EXPIRATION_DAYS} \
    --dnssec=${DDNS_DNSSEC}
