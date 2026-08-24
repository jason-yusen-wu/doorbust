server:
	./scripts/with-env.sh goose up
	./scripts/with-env.sh go run ./cmd

clean:
	./scripts/with-env.sh goose down
