# Trusted Data Space Kernel (可信数据空间内核)

A standardized, lightweight core component for Trusted Data Space, implementing the "Kernel + Extension" architecture design.

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

## Overview

This project implements a **standardized, lightweight Trusted Data Space Kernel** with the following core components:

### Core Kernel Layer

1. **Security Module**
   - Internal CA: Certificate issuance and revocation
   - mTLS Termination: TLS 1.3 based mutual authentication

2. **Control Module**
   - Identity Registry: Manages connector information
   - Policy Engine: Validates request legitimacy based on policies

3. **Circulation Module**
   - Channel Management: Manages data transmission channels
   - Data Relay: Provides encrypted data streaming relay

4. **Evidence Module**
   - Audit Logging: Records hashes of all critical events
   - Chain Storage: Ensures audit logs are tamper-proof

### Extension Layer

- **Connector**: Acts as the gateway for entities to enter the data space, interacting with the kernel via standard gRPC interfaces

---

## Core Concepts

- **Kernel Standardization**: The kernel is a standardized "operating system" providing unified interface specifications
- **Extension Flexibility**: Connectors as extension components can flexibly adapt to various business scenarios
- **Interoperability**: Standard gRPC interfaces enable cross-organization and cross-system interoperability
- **Security Foundation**: Zero-trust security architecture based on mTLS

---

## Architecture

### Overall Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Trusted Data Space                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│    Organization-A                   Organization-B              Organization-C│
│  ┌─────────────┐               ┌─────────────┐               ┌───────────┐ │
│  │   Kernel   │◄─────────────►│   Kernel   │◄─────────────►│   Kernel  │ │
│  │ kernel-1   │    mTLS      │ kernel-2   │    mTLS      │ kernel-3  │ │
│  │ :50051     │               │ :50051     │               │ :50051    │ │
│  │ :50053     │               │ :50053     │               │ :50053    │ │
│  └──────┬──────┘               └──────┬──────┘               └─────┬─────┘ │
│         │                              │                              │       │
│    ┌────┴────┐                    ┌────┴────┐                    ┌────┴────┐  │
│    │Connector │                    │Connector │                    │Connector │  │
│    │  A1     │                    │  B1     │                    │  C1     │  │
│    │  A2     │                    │  B2     │                    │  C2     │  │
│    └─────────┘                    └─────────┘                    └─────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Event Types

### Authentication Events
| Event Type | Description |
|-----------|-------------|
| AUTH_SUCCESS | Connector authentication successful |
| AUTH_FAILED | Connector authentication failed |
| AUTH_TIMEOUT | Authentication timeout |

### Interconnection Events
| Event Type | Description |
|-----------|-------------|
| INTERCONNECT_REQUESTED | Kernel interconnection request initiated |
| INTERCONNECT_APPROVED | Interconnection request approved |
| INTERCONNECT_REJECTED | Interconnection request rejected |
| INTERCONNECT_CLOSED | Interconnection closed |

### Channel Management Events
| Event Type | Description |
|-----------|-------------|
| CHANNEL_PROPOSED | Channel proposal created |
| CHANNEL_ACCEPTED | Channel proposal accepted |
| CHANNEL_REJECTED | Channel proposal rejected |
| CHANNEL_CREATED | Channel officially created |
| CHANNEL_CLOSED | Channel closed |
| CHANNEL_SUBSCRIBED | Channel subscribed |
| CHANNEL_UNSUBSCRIBED | Channel unsubscribed |

### Data Transfer Events
| Event Type | Description |
|-----------|-------------|
| DATA_SEND | Data sent (connector→kernel or kernel→kernel) |
| DATA_RECEIVE | Data received (kernel→connector or kernel→kernel) |

### Permission Management Events
| Event Type | Description |
|-----------|-------------|
| PERMISSION_REQUESTED | Permission change requested |
| PERMISSION_GRANTED | Permission granted |
| PERMISSION_REJECTED | Permission rejected |
| PERMISSION_REVOKED | Permission revoked |

---

## Typical Data Flow Scenarios

### Scenario 1: Single Kernel Data Transfer

```
connector-A ──────► kernel-1 ──────► connector-B

Evidence Records:
1. DATA_SEND: connector-A → kernel-1
2. DATA_RECEIVE: kernel-1 → connector-B
```

### Scenario 2: Cross-Kernel Data Transfer (Two Hops)

```
connector-A ──► kernel-1 ──► kernel-2 ──► connector-U

Evidence Records:
1. DATA_SEND: connector-A → kernel-1         (local send)
2. DATA_SEND: kernel-1 → kernel-2           (cross-kernel forward)
3. DATA_RECEIVE: kernel-1 → kernel-2        (arrived at target kernel)
4. DATA_RECEIVE: kernel-2 → connector-U     (delivered to receiver)
```

---

## Quick Start

### Prerequisites

- Go 1.21+
- Protocol Buffers compiler (protoc)
- OpenSSL (for generating test certificates)
- Make (optional, for simplified building)
- MySQL 5.7+ (optional, file storage is used by default)

### Installation Steps

#### 1. Clone and Install Dependencies

```bash
git clone <repository-url>
cd trusted-space-kernel

# Install Go dependencies
go mod download

# Install protoc plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

#### 2. Generate Protobuf Code

```bash
make proto
```

Or manually:

```bash
mkdir -p api/v1
protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    proto/kernel/v1/*.proto
```

#### 3. Generate Test Certificates

**Linux/Mac:**

```bash
chmod +x scripts/gen_certs.sh
./scripts/gen_certs.sh
```

**Windows (PowerShell):**

```powershell
.\scripts\gen_certs.ps1
```

#### 4. Create Log Directory

```bash
mkdir -p logs
```

#### 5. Build Kernel and Connector

```bash
make all
```

Or manually:

```bash
go build -o bin/kernel.exe ./kernel/cmd
go build -o bin/connector.exe ./connector/cmd
```

### Running Examples

#### Terminal 1: Start Kernel

```bash
./bin/kernel.exe --config config/kernel.yaml
```

Output should include:

```
✓ Registry initialized
✓ Policy engine initialized
✓ Channel manager initialized
✓ Audit log initialized
✓ mTLS configured
✓ gRPC services registered
🚀 Trusted Data Space Kernel started on 0.0.0.0:50051
```

#### Terminal 2: Start Connector

```bash
./bin/connector.exe --config config/connector.yaml
```

The connector will automatically register and obtain a certificate on first run.

---

## Interactive Commands

### Kernel Commands

```bash
# View status
status

# List connectors
connectors or cs

# List channels
channels or ch

# List known kernels
kernels or ks

# Connect to another kernel
connect-kernel <kernel_id> <address> <port>
# Example: connect-kernel kernel-2 192.168.202.136 50053

# Approve interconnection request
approve-request <request_id>

# List pending requests
pending-requests

# Disconnect kernel
disconnect-kernel <kernel_id>

# Multi-hop routes
routes, rt                    # List all routes
load-route <filename>         # Load route configuration
connect-route <route_name>    # Connect to specified route
route-info <route_name>       # View route details

# Exit
exit or quit
```

### Connector Commands

```bash
# List connectors
list or ls

# View connector info
info <connector_id>

# Create channel (proposal)
create --config <config_file>
create --sender <sender_ids> --receiver <receiver_ids> --reason <reason>

# Accept channel proposal
accept <channel_id> <proposal_id>

# Reject channel proposal
reject <channel_id> <proposal_id> --reason <reason>

# Send data
sendto <channel_id> [file_path]
# Example: sendto <channel-id>
#         (enter data, press Enter to send, type END to finish)

# Subscribe to channel
subscribe <channel_id>

# View joined channels
channels or ch

# Query evidence records
query-evidence --channel <channel_id>
query-evidence --connector <connector_id>
query-evidence --flow <flow_id>

# Permission management
request-permission <channel_id> <change_type> <target_id> <reason>
approve-permission <channel_id> <request_id>
reject-permission <channel_id> <request_id> <reason>
list-permissions <channel_id>

# Set status
status [active|inactive|closed]

# Help
help
```

---

## Demo: Cross-Kernel Data Transfer

### 1. Environment Setup

```
┌─────────────────────┐          ┌─────────────────────┐
│   kernel-1          │          │   kernel-2          │
│   192.168.31.155    │◄────────►│   192.168.202.136  │
│   (Windows)         │   mTLS   │   (Linux VM)       │
└─────────┬───────────┘          └─────────┬───────────┘
          │                              │
    connector-A                    connector-U
```

### 2. Start Services

```bash
# Start kernel-1 (Windows)
.\bin\kernel.exe --config .\config\kernel.yaml

# Start kernel-2 (Linux)
./bin/kernel.exe --config config/kernel.yaml
```

### 3. Establish Kernel Interconnection

```bash
# On kernel-1
connect-kernel kernel-2 192.168.202.136 50053

# On kernel-2
approve-request <request_id>
```

### 4. Connectors Join

```bash
# On kernel-1
.\bin\connector.exe --config .\config\connector-A.yaml

# On kernel-2
./bin/connector.exe --config config/connector-U.yaml
```

### 5. Create Cross-Kernel Channel

```bash
# On connector-A
create --sender connector-A --receiver kernel-2:connector-U --reason "Test data transfer"

# On connector-U
accept <channel_id> <proposal_id>
```

### 6. Send Data

```bash
# On connector-A
sendto <channel_id>
# Enter data content
# Type END to finish sending
```

### 7. View Evidence Records

```bash
# Query on kernel
query-evidence --channel <channel_id>
```

---

## Evidence Record Examples

When connector-A sends "hello" data to connector-U through a cross-kernel channel, the following evidence records are generated:

### kernel-1 Evidence Records

| Event Type | source_id | target_id | Description |
|-----------|-----------|-----------|-------------|
| AUTH_SUCCESS | connector-A | kernel-1 | Connector authentication successful |
| INTERCONNECT_REQUESTED | kernel-1 | kernel-2 | Kernel interconnection initiated |
| CHANNEL_CREATED | kernel-1 | kernel-1 | Channel created successfully |
| DATA_SEND | connector-A | kernel-1 | connector-A sends data to kernel |
| DATA_SEND | kernel-1 | kernel-2 | Kernel forwards to target kernel |
| DATA_SEND | kernel-1 | kernel-2 | Forward completion confirmed (with data_hash) |

### kernel-2 Evidence Records

| Event Type | source_id | target_id | Description |
|-----------|-----------|-----------|-------------|
| AUTH_SUCCESS | connector-U | kernel-2 | Connector authentication successful |
| INTERCONNECT_APPROVED | kernel-2 | kernel-1 | Interconnection request approved |
| CHANNEL_CREATED | kernel-2 | kernel-2 | Channel created successfully |
| DATA_RECEIVE | kernel-1 | kernel-2 | Data received from kernel-1 |
| DATA_RECEIVE | kernel-2 | connector-U | Delivered to target connector |

---

## Port Configuration

Each kernel uses three ports for communication:

| Port | Purpose | Description |
|------|---------|-------------|
| 50051 | Main Service | Provides IdentityService, ChannelService, EvidenceService |
| 50052 | Bootstrap | Used for connector certificate application during first registration |
| 50053 | Kernel-to-Kernel | Used for kernel interconnection (P2P communication) |

---

## gRPC Service Interfaces

### 1. IdentityService

```protobuf
service IdentityService {
  rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
  rpc DiscoverConnectors(DiscoverRequest) returns (DiscoverResponse);
  rpc DiscoverCrossKernelConnectors(CrossKernelDiscoverRequest) returns (CrossKernelDiscoverResponse);
  rpc GetConnectorInfo(GetConnectorInfoRequest) returns (GetConnectorInfoResponse);
  rpc SetConnectorStatus(SetConnectorStatusRequest) returns (SetConnectorStatusResponse);
  rpc RegisterConnector(RegisterConnectorRequest) returns (RegisterConnectorResponse);
}
```

### 2. ChannelService

```protobuf
service ChannelService {
  rpc CreateChannel(CreateChannelRequest) returns (CreateChannelResponse);
  rpc StreamData(stream DataPacket) returns (stream TransferStatus);
  rpc SubscribeData(SubscribeRequest) returns (stream DataPacket);
  rpc CloseChannel(CloseChannelRequest) returns (CloseChannelResponse);
  rpc GetChannelInfo(GetChannelInfoRequest) returns (GetChannelInfoResponse);

  // Negotiation
  rpc ProposeChannel(ProposeChannelRequest) returns (ProposeChannelResponse);
  rpc AcceptChannelProposal(AcceptChannelProposalRequest) returns (AcceptChannelProposalResponse);
  rpc RejectChannelProposal(RejectChannelProposalRequest) returns (RejectChannelProposalResponse);

  // Subscription
  rpc RequestChannelSubscription(RequestChannelSubscriptionRequest) returns (RequestChannelSubscriptionResponse);
  rpc ApproveChannelSubscription(ApproveChannelSubscriptionRequest) returns (ApproveChannelSubscriptionResponse);
  rpc RejectChannelSubscription(RejectChannelSubscriptionRequest) returns (RejectChannelSubscriptionResponse);

  // Permission
  rpc RequestPermissionChange(RequestPermissionChangeRequest) returns (RequestPermissionChangeResponse);
  rpc ApprovePermissionChange(ApprovePermissionChangeRequest) returns (ApprovePermissionChangeResponse);
  rpc RejectPermissionChange(RejectPermissionChangeRequest) returns (RejectPermissionChangeResponse);
}
```

### 3. EvidenceService

```protobuf
service EvidenceService {
  rpc SubmitEvidence(EvidenceRequest) returns (EvidenceResponse);
  rpc QueryEvidence(QueryRequest) returns (QueryResponse);
  rpc VerifyEvidenceSignature(VerifySignatureRequest) returns (VerifySignatureResponse);
}
```

### 4. KernelService

```protobuf
service KernelService {
  rpc RegisterKernel(RegisterKernelRequest) returns (RegisterKernelResponse);
  rpc KernelHeartbeat(KernelHeartbeatRequest) returns (KernelHeartbeatResponse);
  rpc DiscoverKernels(DiscoverKernelsRequest) returns (DiscoverKernelsResponse);
  rpc SyncKnownKernels(SyncKnownKernelsRequest) returns (SyncKnownKernelsResponse);
  rpc CreateCrossKernelChannel(CreateCrossKernelChannelRequest) returns (CreateCrossKernelChannelResponse);
  rpc ForwardData(ForwardDataRequest) returns (ForwardDataResponse);
  rpc GetCrossKernelChannelInfo(GetCrossKernelChannelInfoRequest) returns (GetCrossKernelChannelInfoResponse);
  rpc SyncConnectorInfo(SyncConnectorInfoRequest) returns (SyncConnectorInfoResponse);
}
```

---

## Evidence Chain Structure

Each evidence record uses a chain structure:

```
Record N:
{
  EventID: "uuid",
  EventType: "DATA_SEND",
  SourceID: "connector-A",
  TargetID: "kernel-1",
  ChannelID: "channel-uuid",
  DataHash: "sha256(...)",
  Signature: "RSA signature",
  Hash: "sha256(this record)",
  PrevHash: "hash of Record N-1"
}
    │
    ↓ Link
Record N+1:
{
  PrevHash: "hash of Record N",  ← Points to previous
  Hash: "sha256(this record)"
}
```

### Evidence Fields

| Field | Description |
|-------|-------------|
| event_id | Unique event identifier (UUID) |
| event_type | Event type |
| timestamp | Timestamp (microsecond precision) |
| source_id | Event source (connector or kernel) |
| target_id | Target ID (next hop) |
| channel_id | Associated channel ID |
| data_hash | Data hash (optional) |
| signature | Kernel digital signature |
| hash | Record content hash |
| prev_hash | Previous record's hash (hash chain) |
| metadata | Extended metadata (JSON) |

---

## Security Features

| Layer | Mechanism |
|-------|-----------|
| Transport | TLS 1.3 Encryption |
| Authentication | mTLS Mutual Authentication |
| Authorization | Policy Engine Fine-grained Control |
| Audit | Full Evidence Recording |

---

## Project Structure

```
trusted_space_kernel/
├── bin/                        # Compiled executables
│   ├── kernel.exe              # Kernel executable
│   └── connector.exe           # Connector executable
├── certs/                      # Certificate directory
│   ├── ca.crt                  # CA root certificate
│   ├── kernel.crt              # Kernel certificate
│   └── ...
├── channel_configs/            # Channel configuration directory
├── channels/                   # Channel data directory
├── config/                     # Configuration files
│   ├── kernel.yaml            # Kernel configuration
│   ├── connector.yaml          # Connector configuration
│   └── ...
├── connector/                  # Connector implementation
│   ├── cmd/
│   │   └── main.go             # Connector entry
│   └── client/
│       └── connector.go        # Connector client
├── docs/                       # Documentation
│   ├── CORE.md                # Core module documentation
│   └── MULTI_KERNEL_NETWORK.md # Multi-kernel network documentation
├── kernel/                     # Kernel implementation
│   ├── bin/
│   ├── circulation/            # Circulation module
│   │   ├── channel_manager.go  # Channel manager
│   │   └── channel_config.go  # Channel configuration
│   ├── cmd/
│   │   └── main.go            # Kernel entry
│   ├── control/                # Control module
│   │   ├── policy.go           # Policy engine
│   │   └── registry.go        # Identity registry
│   ├── database/               # Database module
│   │   ├── evidence_store.go   # Evidence storage
│   │   └── mysql.go           # MySQL support
│   ├── evidence/              # Evidence module
│   │   └── audit_log.go       # Audit log
│   ├── security/              # Security module
│   │   ├── ca.go              # CA certificate management
│   │   ├── mtls.go            # mTLS configuration
│   │   └── signing.go         # Digital signature
│   └── server/                 # gRPC services
│       ├── channel_service.go  # Channel service
│       ├── identity_service.go # Identity service
│       ├── evidence_service.go # Evidence service
│       ├── kernel_service.go   # Kernel service
│       ├── multi_kernel_manager.go    # Multi-kernel manager
│       └── multi_hop_config.go        # Multi-hop configuration
├── kernel_configs/             # Multi-hop route configuration
├── proto/                      # Protocol Buffers definitions
│   └── kernel/
│       └── v1/
│           ├── kernel.proto    # Kernel-to-kernel communication
│           ├── channel.proto   # Channel service
│           ├── identity.proto  # Identity service
│           └── evidence.proto  # Evidence service
├── scripts/                    # Script tools
│   ├── gen_certs.sh/ps1       # Certificate generation
│   ├── quick_start.sh/ps1     # Quick start
│   └── package_all.sh/ps1     # Packaging tool
├── go.mod                      # Go module definition
├── go.sum                      # Dependency checksum
└── Makefile                    # Build script
```

---

## Configuration

### Kernel Configuration (config/kernel.yaml)

```yaml
server:
  address: "0.0.0.0"
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  server_cert_path: "certs/kernel.crt"
  server_key_path: "certs/kernel.key"

evidence:
  persistent: true
  log_file_path: "logs/audit.log"

policy:
  default_allow: true
```

### Connector Configuration (config/connector.yaml)

```yaml
connector:
  id: "connector-A"
  entity_type: "data_source"

kernel:
  address: "localhost"
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  client_cert_path: "certs/connector-A.crt"
  client_key_path: "certs/connector-A.key"
  server_name: "trusted-data-space-kernel"
```

---

## Build and Package

### Build

```bash
# Build kernel
make build-kernel

# Build connector
make build-connector

# Build all
make build
```

### Package

```bash
# Linux/Mac
./scripts/package_all.sh 1.0.0 linux-amd64 all

# Windows
.\scripts\package_all.ps1 -Version 1.0.0 -Platform windows-amd64 -Target all
```

---

## Troubleshooting

| Scenario | Solution |
|----------|----------|
| Connection lost | Heartbeat detection, update status, retain info for reconnect |
| Certificate error | Check certificate path, validity, CA signature |
| Permission denied | Check ACL policy configuration |
| Evidence failure | Auto-fallback to file storage |
| Cross-kernel forward failure | Retry mechanism, retain original data |

---

## Production Deployment Recommendations

1. **Certificate Management**
   - Use formal PKI infrastructure
   - Regularly rotate certificates
   - Implement Certificate Revocation List (CRL)

2. **Log Storage**
   - Persist audit logs to distributed storage
   - Consider blockchain integration for evidence

3. **High Availability**
   - Deploy multiple kernel instances
   - Use load balancer
   - Implement failover mechanism

4. **Monitoring and Alerting**
   - Integrate Prometheus/Grafana
   - Monitor connector online status
   - Alert on abnormal authentication attempts

5. **Security Hardening**
   - Enable firewall rules
   - Implement IP whitelist
   - Regular security audits

---

## License

MIT License - See LICENSE file

---

## Contributing

Welcome to submit Issues and Pull Requests!

---

## Contact

- Project Maintainer: [Your Name]
- Email: [your@email.com]

---

**Note**: The certificates generated by this project are for testing purposes only. Do not use in production environments.
