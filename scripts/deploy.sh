#!/usr/bin/env bash
# Deploys the storage server to the two EC2 nodes created by infra/aws.
# Run from the repo root, after `terraform apply`.
set -euo pipefail

TF="tools/terraform -chdir=infra/aws"
NODE_A=$($TF output -raw node_a_public_ip)
NODE_B=$($TF output -raw node_b_public_ip)

if [[ -z "$NODE_A" || -z "$NODE_B" ]]; then
    echo "no node IPs from terraform; run terraform apply first" >&2
    exit 1
fi

SSH_OPTS="-o StrictHostKeyChecking=accept-new"

deploy_node() {
    local ip=$1
    echo "== deploying to $ip"

    # Stop an old server first. Linux won't let scp overwrite a running binary.
    ssh $SSH_OPTS "ubuntu@$ip" 'pkill -x storage || true'
    scp $SSH_OPTS bin/storage "ubuntu@$ip:~/"

    # Set up the local NVMe disk and start the server, on the node itself.
    ssh $SSH_OPTS "ubuntu@$ip" bash -s<<'EOF'
set -euo pipefail

DISK=$(lsblk -dno NAME,MODEL | awk '/Instance Storage/ {print "/dev/" $1}')
if [[ -z "$DISK" ]]; then
    echo "no instance storage disk found" >&2
    exit 1
fi

if ! mountpoint -q /mnt/data; then
    sudo mkfs.ext4 -q "$DISK"
    sudo mkdir -p /mnt/data
    sudo mount "$DISK" /mnt/data
    sudo chown ubuntu:ubuntu /mnt/data
fi

nohup ./storage -data-dir /mnt/data > storage.log 2>&1 < /dev/null &
sleep 1
curl -fsS http://localhost:8080/healthz
echo
EOF
}

wait_for_ssh() {
    local ip=$1
    until ssh $SSH_OPTS -o ConnectTimeout=5 "ubuntu@$ip" true 2>/dev/null; do
        echo "waiting for ssh on $ip"
        sleep 5
    done
}

echo "building bin/storage"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/storage ./cmd/storage

for ip in "$NODE_A" "$NODE_B"; do
    wait_for_ssh "$ip"
    deploy_node "$ip"
done

NODE_B_PRIVATE=$($TF output -raw node_b_private_ip)
echo "== checking node A -> node B over the private network"
ssh $SSH_OPTS "ubuntu@$NODE_A" curl -fsS "http://$NODE_B_PRIVATE:8080/healthz"
echo
echo "== done"
