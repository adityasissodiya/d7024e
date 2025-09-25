# ---------- build stage ----------
FROM golang:1.22-alpine AS build
WORKDIR /src

# Build in GOPATH mode so "d7024e/kademlia" imports resolve
ENV GO111MODULE=off

# Bring in the whole repo (compose build context will be the repo root)
COPY . .

# Map the package to GOPATH so imports like "d7024e/kademlia" work
# Our code lives at labs/kademlia, but the import path expects /go/src/d7024e/kademlia
RUN mkdir -p /go/src/d7024e \
 && cp -a labs/kademlia /go/src/d7024e/kademlia

# Sanity check (optional but handy)
RUN test -f labs/kademlia/cmd/cli/main.go || (echo "CLI not found; wrong build context?" && ls -R && exit 1)

# Build the CLI runner
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kcli ./labs/kademlia/cmd/cli

# ---------- runtime stage ----------
FROM alpine:3.20
RUN apk add --no-cache bash ca-certificates bind-tools
WORKDIR /app
COPY --from=build /out/kcli /app/kcli
COPY labs/docker/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh && sed -i 's/\r$//' /app/entrypoint.sh

EXPOSE 9000/udp
ENTRYPOINT ["/app/entrypoint.sh"]
