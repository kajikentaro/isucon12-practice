include .env

build:
	cd webapp/go/ && go build -o isuports ./cmd/isuports

push1:
	ssh isucon@${PROD_SERVER} "sudo systemctl stop isuports.service"
	scp webapp/go/isuports.go isucon@${PROD_SERVER}:/home/isucon/webapp/go/
	ssh isucon@${PROD_SERVER} "sudo systemctl start isuports.service"

pull_digest1:
	mkdir -p digest
	scp isucon@${PROD_SERVER}:/tmp/*.digest digest/
