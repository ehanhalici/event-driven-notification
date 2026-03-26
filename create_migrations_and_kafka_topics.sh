#!/bin/bash

docker run --rm -v $(pwd)/migrations:/migrations --network host migrate/migrate     -path=/migrations/ -database "postgres://insider:secretpassword@localhost:5432/notifications_db?sslmode=disable" up

docker exec -it insider_kafka /opt/kafka/bin/kafka-topics.sh --create --bootstrap-server localhost:9092 --replication-factor 1 --partitions 3 --topic notifications-high

docker exec -it insider_kafka /opt/kafka/bin/kafka-topics.sh --create --bootstrap-server localhost:9092 --replication-factor 1 --partitions 3 --topic notifications-normal

docker exec -it insider_kafka /opt/kafka/bin/kafka-topics.sh --create --bootstrap-server localhost:9092 --replication-factor 1 --partitions 3 --topic notifications-low

docker exec -it insider_kafka /opt/kafka/bin/kafka-topics.sh --create --bootstrap-server localhost:9092 --replication-factor 1 --partitions 3 --topic notifications-dlq
