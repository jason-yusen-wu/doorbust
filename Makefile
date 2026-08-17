server:
	docker compose up -d
	goose up 
	go run cmd/*.go

clean:
	goose down
	docker compose down