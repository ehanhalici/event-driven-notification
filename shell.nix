# shell.nix
{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  buildInputs = with pkgs; [
    # Go derleyicisi ve araçları
    go
    gopls
    gotools
    golangci-lint

    # Veritabanı ve altyapı araçları
    docker-compose # Kafka, Redis ve Postgres'i ayağa kaldırmak için
    
    # Test ve debug için
    jq
    curl
    go-migrate
    go-swag
    code-cursor
  ];

  shellHook = ''
    echo "Insider Notification System dev environment loaded!"
    echo "Go version: $(go version)"

    newgrp docker
    export SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt
    export GOPATH=$PWD/.go
    export PATH=$GOPATH/bin:$PATH
  '';
}
