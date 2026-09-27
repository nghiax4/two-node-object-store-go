terraform {
  required_version = ">= 1.16"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.66"
    }
  }
}

provider "aws" {
  region = "us-east-1"

  default_tags {
    tags = {
      Project = "two-node-object-store"
    }
  }
}

resource "aws_key_pair" "bench" {
  key_name   = "two-node-object-store"
  public_key = file(pathexpand("~/.ssh/id_ed25519.pub"))
}

variable "my_ip" {
  description = "Your public IP, allowed to SSH in. Pass it with -var."
  type        = string
}

resource "aws_security_group" "bench" {
  name        = "two-node-object-store"
  description = "SSH from my IP, port 8080 between the nodes"

  ingress {
    description = "SSH from my laptop"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["${var.my_ip}/32"]
  }

  ingress {
    description = "Object store HTTP between the nodes"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    self        = true
  }

  egress {
    description = "All outbound traffic"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical, the company behind Ubuntu

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

resource "aws_instance" "node_a" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = "c6id.large"
  availability_zone      = "us-east-1a"
  key_name               = aws_key_pair.bench.key_name
  vpc_security_group_ids = [aws_security_group.bench.id]

  tags = {
    Name = "node-a"
  }
}

resource "aws_instance" "node_b" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = "c6id.large"
  availability_zone      = "us-east-1a"
  key_name               = aws_key_pair.bench.key_name
  vpc_security_group_ids = [aws_security_group.bench.id]

  tags = {
    Name = "node-b"
  }
}

output "node_a_public_ip" {
  value = aws_instance.node_a.public_ip
}

output "node_a_private_ip" {
  value = aws_instance.node_a.private_ip
}

output "node_b_public_ip" {
  value = aws_instance.node_b.public_ip
}

output "node_b_private_ip" {
  value = aws_instance.node_b.private_ip
}
