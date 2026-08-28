FROM gcr.io/distroless/static-debian12

WORKDIR /app
COPY build/server /app/server

CMD ["/app/server"]
