# Use official go image
FROM golang:1.25

# Install Java 25 (required by latest Minecraft server jar)
RUN apt-get update && apt-get install -y wget && \
    ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
      JDK_URL="https://download.oracle.com/java/25/latest/jdk-25_linux-x64_bin.tar.gz"; \
    elif [ "$ARCH" = "arm64" ]; then \
      JDK_URL="https://download.oracle.com/java/25/latest/jdk-25_linux-aarch64_bin.tar.gz"; \
    fi && \
    wget -q "$JDK_URL" -O jdk.tar.gz && \
    mkdir -p /opt/java && \
    tar -xzf jdk.tar.gz -C /opt/java --strip-components=1 && \
    rm jdk.tar.gz && \
    apt-get clean

ENV JAVA_HOME=/opt/java
ENV PATH="$JAVA_HOME/bin:$PATH"

# Set /app as work dir.
WORKDIR /app

# Copy go module files and install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Install Air for hot reload
RUN curl -sSfL https://raw.githubusercontent.com/air-verse/air/master/install.sh | sh -s -- -b /usr/local/bin

# Copy rest of code
COPY . .

# Generate swagger docs and build go app
RUN go generate ./... && go build -o server .

# Expose ports
EXPOSE 8080 25565
EXPOSE 24454/udp

# Run app
CMD ["./server"]