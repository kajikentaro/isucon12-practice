include .env

build:
	cd webapp/go/ && go build -o isuports ./cmd/isuports

push1:
	ssh isucon@${PROD_SERVER} "sudo systemctl stop isuports.service"
	scp webapp/go/isuports.go isucon@${PROD_SERVER}:/home/isucon/webapp/go/
	scp webapp/go/go.* isucon@${PROD_SERVER}:/home/isucon/webapp/go/
	ssh isucon@${PROD_SERVER} "sudo systemctl start isuports.service"

pull_digest1:
	mkdir -p digest
	scp isucon@${PROD_SERVER}:/tmp/*.digest digest/

time = ${shell date '+%m%d_%H%M'}
bench1:
	ssh isucon@${PROD_SERVER} "sudo cat /var/log/nginx/access.log | sudo tee -a /var/log/nginx/access_${time}.log" > /dev/null
	ssh isucon@${PROD_SERVER} "sudo cp /dev/null /var/log/nginx/access.log"
	cd ./bench && go run cmd/bench/main.go -target-addr ${PROD_SERVER}:443


matching_group = '/api/player/competition/[a-z0-9\-]+/ranking,/api/player/player/[a-z0-9\-]+,/api/player/competition/[a-z0-9\-]+/ranking,/api/organizer/competition/[a-z0-9\-]+/score,/api/organizer/competition/[a-z0-9\-]+/finish,/api/organizer/player/[a-z0-9\-]+/disqualified'
pull_alp1:
	ssh isucon@${PROD_SERVER} "sudo cat /var/log/nginx/access.log | alp json --sort sum -r -m ${matching_group} -o count,method,uri,min,avg,max,sum > /tmp/${time}.alp"
	scp isucon@${PROD_SERVER}:/tmp/${time}.alp digest/
	
