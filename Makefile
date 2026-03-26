.PHONY: run-api run-worker run-relay test up down migrate-up

up:
	docker-compose up -d

down:
	docker-compose down

migrate-up:
	migrate -path migrations -database "postgres://insider:secretpassword@localhost:5432/notifications_db?sslmode=disable" up

test:
	go test -v ./...

run-api:
	go run cmd/api/main.go

run-worker:
	go run cmd/worker/main.go

run-relay:
	go run cmd/relay/main.go
