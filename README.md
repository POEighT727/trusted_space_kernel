# Trusted Data Space Kernel (可信数据空间内核)

A **standardized, lightweight** core component for Trusted Data Space, implementing the **Kernel + Extension** dual-component architecture. The kernel provides standardized interfaces and services, while extension components (Connectors) flexibly adapt to various business scenarios.

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

---

## Core Concepts

- **Kernel Standardization**: The kernel is a standardized "operating system" providing unified interface specifications
- **Extension Flexibility**: Connectors as extension components can flexibly adapt to various business scenarios
- **Interoperability**: Standard gRPC interfaces and P2P direct connection protocol enable cross-organization and cross-system interoperability
- **Security Foundation**: Zero-trust security architecture based on mTLS with RSA-PSS digital signatures

---

## Architecture

### Overall Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                           Trusted Data Space                                        │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│    Organization-A                      Organization-B                   Organization-C│
│  ┌─────────────┐                ┌─────────────┐                   ┌───────────┐    │
│  │   Kernel   │◄───────────────►│   Kernel   │◄─────────────────►│   Kernel  │    │
│  │ kernel-1   │      mTLS       │ kernel-2   │       mTLS         │ kernel-3  │    │
│  │ :50051     │                │ :50051     │                    │ :50051    │    │
│  │ :50052     │                │ :50052     │                    │ :50052    │    │
│  │ :50053     │                │ :50053     │                    │ :50053    │    │
│  │ :50055     │                │ :50055     │                    │ :50055    │    │
│  └──────┬──────┘                └──────┬──────┘                   └─────┬─────┘    │
│         │                               │                                │           │
│    ┌────┴────┐                     ┌────┴────┐                     ┌────┴────┐     │
│    │Connector│                     │Connector│                     │Connector│     │
│    │  A1     │                     │  B1     │                     │  C1     │     │
│    │  A2     │                     │  B2     │                     │  C2     │     │
│    └─────────┘                     └─────────┘                     └─────────┘     │
│                                                                                     │
│  ┌────────────────────────────── P2P Operator Direct ──────────────────────────┐ │
│  │  Operator ↔ Operator (TCP Direct, Custom Binary Protocol, Sync Connectors)    │ │
│  └───────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

### Three-Layer Architecture

```
┌───────────────────────────────────────────────────────────────────────┐
│                    Entity Extension Layer                               │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                             │
│  │Database  │  │Algorithm │  │Application│                            │
│  │Connector│  │Connector│  │Connector  │                            │
│  └────┬────┘  └────┬────┘  └────┬────┘                               │
└───────┼────────────┼────────────┼─────────────────────────────────────┘
        │ mTLS       │ mTLS       │ mTLS
┌───────┼────────────┼────────────┼─────────────────────────────────────┐
│       │       Trusted Interaction Layer (gRPC/mTLS)      │              │
│       ↓            ↓            ↓            ↓              │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                  Kernel Layer                            │  │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │  │
│  │  │ Security │  │ Control  │  │Circulation│  │ Evidence │  │  │
│  │  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │  │
│  │  ┌──────────┐  ┌──────────┐                             │  │
│  │  │Multi-Kernel│ │Multi-Hop│                             │  │
│  │  └──────────┘  └──────────┘                             │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘

Note: Circulation Module includes:
  - Channel Management: logical data pipeline with pub-sub pattern
  - TempChat: lightweight temporary messaging without formal channels
  - Operator Peer: direct TCP connections for connector sync and message relay
```

---

## Core Modules

### 1. Security Module

Implements **mTLS (Mutual TLS)** for zero-trust security architecture:

- **Root CA**: Self-signed root certificate as trust anchor
- **Server Certificate**: Certificate held by kernel server
- **Client Certificate**: Each connector holds a certificate with CN matching connector ID
- **Dynamic Registration**: Connectors can apply for certificates on first connection (Bootstrap service, port 50052)
- **RSA-PSS Digital Signature**: RSA-PSS algorithm for signing evidence records
- **SHA-256 Hashing**: SHA-256 algorithm for data hashing

### 2. Control Module

Manages connector identity and data transfer permissions:

- **Identity Registry** (`registry.go`): Manages complete lifecycle of connectors
- **Heartbeat Mechanism**: Client sends heartbeat every 15s, server detects offline after 30s
- **Policy Engine** (`policy.go`): Rule-based access control with exact match and wildcard support
- **Connector Status**: Supports `active` / `inactive` / `closed` states

### 3. Circulation Module

Manages data transmission through **Channels (Channel)**, including **TempChat** and **P2P Operator Peer**:

#### 3.1 Channel Management

- **Channel**: Logical data transmission pipeline providing relay, buffering and distribution
- **Pub-Sub Pattern**: Sender pushes data to channel, receiver subscribes to get data
- **Negotiation**: Two-phase channel creation (propose-accept)
- **Cross-Kernel Channel**: Supports cross-kernel channel creation and data forwarding
- **Multi-Hop Routing**: Supports multi-hop routing configuration
- **Subscription Approval**: Sender can approve receiver's subscription request

#### 3.2 TempChat

Provides lightweight temporary session capability for connector communication without formal channels (e.g., ops messages, debug commands):

- **Session Registration**: Connectors register temporary sessions with ID and session key
- **Heartbeat Keep-Alive**: Sessions stay alive through heartbeat
- **Message Exchange**: Real-time bidirectional messaging
- **Cross-Kernel Forwarding**: Messages route across kernels to target connector's kernel
- **Remote Connector Cache**: Caches remote connector list during cross-kernel communication

**Architecture**:

```
Connector ↔ TempChatService (gRPC, port 50055) ↔ TempChatManager (in-memory)
                                    ↓
                    Cross-kernel forwarding (gRPC ForwardTempMessage / P2P RelayMessage)
```

#### 3.3 P2P Operator Peer — Part of Circulation

Implements direct TCP connections between operators for syncing online connector list and relaying temporary messages, avoiding gRPC dependency:

- **On-Demand Connection**: No pre-connection, establishes connection when needed
- **Full-Duplex Multiplexing**: Active and passive connections unified as `PeerClient`
- **Auto-Reconnect**: Built-in auto-reconnect mechanism
- **Connector Sync**: `SyncConnectors` message syncs online connector list
- **Message Relay**: `RelayMessage` relays temporary messages between connectors
- **TempChat Integration**: Messages delivered to `TempChatManager.DeliverMessage()`

**Custom TCP Protocol** (Binary Header + JSON Payload):

```
┌──────────────────────────────────────┐
│  Header (28 bytes)                   │
│  Magic(4) + Ver(1) + Type(1)        │
│  Flags(1) + Reserved(1)              │
│  Len(4) + TraceID(16)                │
├──────────────────────────────────────┤
│  Payload (variable, ≤8MB)            │
│  JSON format message content          │
└──────────────────────────────────────┘
```

### 4. Evidence Module

Uses **timestamp-ordered hash chain records**:

- **60+ Event Types**: Covers data transfer, channel management, permission, security, interconnection events
- **Integrity Protection**: RSA-PSS digital signature + hash chain ensures tamper-proof
- **Multi-Backend**: File storage / MySQL / hybrid storage
- **Business Hash Chain**: Connectors can build signed hash chains of local business data
- **Auto-Fallback**: Automatic fallback to file storage on evidence failure

### 5. Multi-Kernel Module

Supports multiple kernels forming P2P distributed network:

- **Kernel Discovery**: Sync known kernel list via `SyncKnownKernels`
- **Direct Connection**: Kernels establish direct gRPC connections (port 50053)
- **Multi-Hop Routing**: Supports multi-hop route configuration
- **Heartbeat Maintenance**: 60s heartbeat detects connection status
- **Cross-Kernel Channel**: Supports cross-kernel data transmission channels
- **Connector Info Sync**: Cross-kernel sync of connector online status and basic info

---

## Port Configuration

Each kernel uses four ports:

| Port | Purpose | Description |
|------|---------|-------------|
| 50051 | Main Service | IdentityService, ChannelService, EvidenceService, BusinessChainService |
| 50052 | Bootstrap | Connector certificate application during first registration (no mTLS) |
| 50053 | Kernel-to-Kernel | Kernel interconnection (mTLS encrypted gRPC) |
| 50055 | TempChat | Connector temporary session communication (gRPC) |

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

### 5. BusinessChainService

```protobuf
service BusinessChainService {
  rpc SubmitHashChain(SubmitHashChainRequest) returns (SubmitHashChainResponse);
  rpc QueryHashChain(QueryHashChainRequest) returns (QueryHashChainResponse);
  rpc VerifyHashChain(VerifyHashChainRequest) returns (VerifyHashChainResponse);
}
```

### 6. TempChatService

```protobuf
service TempChatService {
  rpc RegisterSession(RegisterSessionRequest) returns (RegisterSessionResponse);
  rpc HeartbeatSession(HeartbeatSessionRequest) returns (HeartbeatSessionResponse);
  rpc UnregisterSession(UnregisterSessionRequest) returns (UnregisterSessionResponse);
  rpc ListOnlineConnectors(ListOnlineConnectorsRequest) returns (ListOnlineConnectorsResponse);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc ReceiveMessage(ReceiveMessageRequest) returns (stream Message);
  rpc ForwardTempMessage(ForwardTempMessageRequest) returns (ForwardTempMessageResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
}
```

---

## Event Types

### Authentication Events

| Event Type | Description |
|------------|-------------|
| AUTH_SUCCESS | Connector authentication successful |
| AUTH_FAILED | Connector authentication failed |
| AUTH_TIMEOUT | Authentication timeout |

### Interconnection Events

| Event Type | Description |
|------------|-------------|
| INTERCONNECT_REQUESTED | Kernel interconnection request initiated |
| INTERCONNECT_APPROVED | Interconnection request approved |
| INTERCONNECT_REJECTED | Interconnection request rejected |
| INTERCONNECT_CLOSED | Interconnection closed |

### Channel Management Events

| Event Type | Description |
|------------|-------------|
| CHANNEL_PROPOSED | Channel proposal created |
| CHANNEL_ACCEPTED | Channel proposal accepted |
| CHANNEL_REJECTED | Channel proposal rejected |
| CHANNEL_CREATED | Channel officially created |
| CHANNEL_CLOSED | Channel closed |
| CHANNEL_SUBSCRIBED | Channel subscribed |
| CHANNEL_UNSUBSCRIBED | Channel unsubscribed |

### Data Transfer Events

| Event Type | Description |
|------------|-------------|
| DATA_SEND | Data sent (connector→kernel or kernel→kernel) |
| DATA_RECEIVE | Data received (kernel→connector or kernel→kernel) |

### Permission Management Events

| Event Type | Description |
|------------|-------------|
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

### Scenario 3: Temporary Session Message

```
connector-A ──► kernel-1 (TempChat) ──► kernel-2 (TempChat) ──► connector-U

1. Connector registers TempChat session
2. Sender sends temporary message via gRPC SendMessage
3. If target connector on remote kernel, ForwardTempMessage routes cross-kernel
4. Receiver receives message via ReceiveMessage stream
```

---

## Project Structure

```
trusted_space_kernel/
├── bin/                              # Compiled executables
│   ├── kernel.exe                    # Kernel executable
│   └── connector.exe                 # Connector executable
├── certs/                            # Certificate directory
│   ├── ca.crt / ca.key              # CA root certificate
│   ├── kernel.crt / kernel.key      # Kernel certificate
│   └── connector-{A,B,C,X}*.crt/.key # Connector certificates
├── config/                           # Configuration files
│   ├── kernel.yaml                   # Kernel main config
│   ├── connector.yaml               # Connector A config
│   ├── connector-B.yaml             # Connector B config
│   ├── connector-C.yaml             # Connector C config
│   └── connector-X.yaml             # Connector X config
├── channels/                         # Channel data directory
│   └── {connector-id}/             # Subdirectory per connector
├── kernel_configs/                    # Multi-hop route config (JSON)
├── connector/                         # Connector implementation
│   ├── cmd/
│   │   └── main.go                   # Connector entry (2118 lines)
│   ├── client/
│   │   ├── connector.go             # Connector core client (2196 lines)
│   │   └── tls.go                   # TLS config loader
│   ├── database/
│   │   ├── store.go                 # Local data storage
│   │   ├── hash_chain.go            # Business hash chain (RSA signing)
│   │   └── mysql.go                  # MySQL support
│   └── tempchat/
│       └── client.go                # TempChat client
├── kernel/                           # Kernel implementation
│   ├── cmd/
│   │   └── main.go                   # Kernel entry (1505 lines)
│   ├── circulation/                   # Circulation module
│   │   ├── channel_manager.go       # Channel manager
│   │   └── channel_config.go         # Channel config manager
│   ├── control/                      # Control module
│   │   ├── registry.go              # Identity registry
│   │   └── policy.go                 # Policy engine
│   ├── database/                      # Database module
│   │   ├── mysql.go                  # MySQL connection
│   │   ├── evidence_store.go        # Evidence storage
│   │   └── business_chain_store.go  # Business chain storage
│   ├── evidence/                      # Evidence module
│   │   └── audit_log.go             # Audit log (60+ event types)
│   ├── security/                      # Security module
│   │   ├── ca.go                     # CA certificate management
│   │   ├── mtls.go                  # mTLS config generator
│   │   └── signing.go                # RSA-PSS digital signature
│   ├── server/                        # gRPC service implementation
│   │   ├── channel_service.go        # Channel service (20+ methods)
│   │   ├── identity_service.go       # Identity service
│   │   ├── evidence_service.go       # Evidence service
│   │   ├── kernel_service.go          # Kernel-to-kernel service
│   │   ├── multi_kernel_manager.go   # Multi-kernel P2P manager
│   │   ├── multi_hop_config.go       # Multi-hop config manager
│   │   └── business_chain_manager.go # Business chain service
│   ├── tempchat/                      # TempChat module
│   │   ├── manager.go                # Session manager
│   │   └── service.go                # gRPC service
│   └── operator_peer/                 # P2P Operator Peer
│       ├── packet.go                  # P2P packet codec
│       ├── server.go                  # P2P server
│       ├── client.go                  # P2P client (with auto-reconnect)
│       └── manager.go                 # P2P manager
├── proto/                            # Protocol Buffers definitions
│   └── kernel/
│       └── v1/
│           ├── kernel.proto          # Kernel-to-kernel service
│           ├── channel.proto          # Channel service
│           ├── identity.proto         # Identity service
│           ├── evidence.proto         # Evidence service
│           ├── business_chain.proto   # Business chain service
│           ├── tempchat.proto        # TempChat service
│           └── operator_peer.proto   # P2P Operator Peer protocol
├── scripts/                           # Script tools
│   ├── gen_certs.sh / gen_certs.ps1 # Certificate generation
│   └── package_all.sh / package_all.ps1 # Packaging
├── docs/                              # Documentation
│   └── CORE.md                       # Core module design doc
├── go.mod                            # Go module definition
├── go.sum                            # Dependency checksum
├── Makefile                          # Build script
└── README.md / README_CN.md         # English/Chinese documentation
```

---

## Quick Start

### Prerequisites

- **Go**: 1.21 or higher
- **Database**: MySQL 5.7+ (optional, file storage by default)
- **OS**: Linux / macOS / Windows

### 1. Generate Certificates

```bash
# Linux/Mac
./scripts/gen_certs.sh

# Windows
.\scripts\gen_certs.ps1
```

### 2. Database Setup (Optional)

```sql
CREATE DATABASE IF NOT EXISTS trusted_space CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

Then modify database config in `config/kernel.yaml`.

### 3. Start Kernel

```bash
# Interactive mode (recommended)
./bin/kernel.exe --config config/kernel.yaml

# Daemon mode
./bin/kernel.exe --config config/kernel.yaml -daemon
```

### 4. Start Connector

```bash
# First run auto-registers and obtains certificate
./bin/connector.exe --config config/connector.yaml
```

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

# Approve interconnection request
approve-request <request_id>

# List pending requests
pending-requests

# Disconnect kernel
disconnect-kernel <kernel_id>

# Multi-hop routes
routes, rt                    # List all routes
load-route <filename>         # Load route config
connect-route <route_name>    # Connect specified route
route-info <route_name>       # View route details

# P2P Operator Peer commands
connect-peer <kernel_id> <address> <port>  # Connect to operator
disconnect-peer <kernel_id>                 # Disconnect operator
peers or ps                               # List connected operators
peer-info <kernel_id>                      # View operator details

# TempChat commands
list-sessions                    # List current sessions
tempchat-connectors              # List connectors with TempChat sessions

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

# Accept/reject channel proposal
accept <channel_id> <proposal_id>
reject <channel_id> <proposal_id> --reason <reason>

# Send data
sendto <channel_id> [file_path]

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

# TempChat
tempchat list                  # List registered TempChat connectors
tempchat send <connector_id> <message>  # Send temporary message
tempchat receive               # Receive temporary messages

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
# kernel-1 (Windows)
.\bin\kernel.exe --config .\config\kernel.yaml

# kernel-2 (Linux)
./bin/kernel.exe --config config/kernel.yaml
```

### 3. Establish Interconnection

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
# Enter data, type END to finish
```

### 7. View Evidence

```bash
# On kernel
query-evidence --channel <channel_id>
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
  Signature: "RSA-PSS signature",
  Hash: "sha256(this record)",
  PrevHash: "hash of Record N-1"
}
    │
    ↓ Link
Record N+1:
{
  PrevHash: "hash of Record N",
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
| signature | Kernel RSA-PSS digital signature |
| hash | Record content hash |
| prev_hash | Previous record's hash (hash chain) |
| metadata | Extended metadata (JSON) |

---

## Business Hash Chain

Connectors can build local business data hash chains for data non-repudiation:

```
Data Record:
{
  DataID: "uuid",
  Data: "business data content",
  Hash: "sha256(Data)",
  Timestamp: "2026-04-10T10:00:00Z",
  ConnectorID: "connector-A",
  Signature: "RSA-PSS signature on Hash"
}

Chain Structure:
Record 1 → Record 2 → Record 3 → ... → Record N
  ↓         ↓         ↓               ↓
PrevHash ← Hash ← Hash ← ... ← Hash ← PrevHash
```

**gRPC Interface** (`BusinessChainService`):
- `SubmitHashChain`: Submit business hash chain records
- `QueryHashChain`: Query business hash chain
- `VerifyHashChain`: Verify hash chain integrity

---

## P2P Operator Peer Protocol

### Protocol Format

Custom TCP protocol with binary header + JSON payload:

```
┌────────────────────────────────────────────────────────────────┐
│  Header (28 bytes)                                             │
│  ┌────────┬──────┬────────┬────────┬──────────────────────────┐ │
│  │ Magic  │ Ver  │ Type  │ Flags │ Reserved (1 byte)        │ │
│  │ 4 bytes│1 byte│1 byte │1 byte │                          │ │
│  ├────────┴──────┴────────┴────────┴──────────────────────────┤ │
│  │ Len (4 bytes, big-endian)    │ TraceID (16 bytes)          │ │
│  └─────────────────────────────┴───────────────────────────────┘ │
├────────────────────────────────────────────────────────────────┤
│  Payload (variable, ≤8MB)                                      │
│  JSON format message content                                    │
└────────────────────────────────────────────────────────────────┘
```

### Message Types

| Type | Name | Purpose |
|------|------|---------|
| 0x01 | Handshake | Handshake to negotiate kernel ID |
| 0x02 | SyncConnectors | Sync online connector list |
| 0x03 | RelayMessage | Relay temporary messages between connectors |
| 0x04 | Heartbeat | P2P heartbeat keep-alive |
| 0x05 | Disconnect | Notify disconnection |

---

## Deployment Topology

### Single Node (Test Environment)

```
┌──────────────────────────────────────┐
│          Kernel                       │
│  50051 (Main)  50052 (Bootstrap)     │
│  50053 (Kernel)  50055 (TempChat)     │
└──────────────────────────────────────┘
         ↑ ↑ ↑ ↑
    Conn-A  Conn-B  Conn-C
```

### Cluster (Production)

```
              ┌─────────────┐
              │Load Balancer │
              └──────┬───────┘
                     │
        ┌────────────┼────────────┐
        ↓            ↓            ↓
   Kernel-1      Kernel-2     Kernel-3
   (50051-53)    (50051-53)   (50051-53)
   (50055)       (50055)      (50055)
        └────────────┴────────────┘
                     │
            ┌────────┴────────┐
            ↓                 ↓
      PostgreSQL         Blockchain
      (Audit Logs)       (Evidence)
```

---

## Performance Features

- **Streaming**: gRPC bidirectional streaming, large file chunked transfer
- **Buffer Queue**: 1000 packets buffer per channel
- **Concurrent Subscription**: Multiple subscribers per channel
- **Batch Write**: Batch persistence for evidence operations
- **Connection Pool**: Reuse gRPC connections
- **Auto-Reconnect**: P2P client built-in auto-reconnect

---

## Security Features

| Layer | Mechanism |
|-------|-----------|
| Transport | TLS 1.3 Encryption |
| Authentication | mTLS Mutual Authentication (Bootstrap for first registration) |
| Authorization | Policy Engine fine-grained control (exact match + wildcard) |
| Audit | Full evidence recording (60+ event types) |
| Signature | RSA-PSS Digital Signature + SHA-256 Hash Chain |

---

## Build and Package

### Build

```bash
# Generate Protobuf code
make proto

# Generate test certificates
make certs

# Build kernel
make kernel

# Build connector
make connector

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
| P2P connection lost | Auto-reconnect mechanism |
| TempChat session expired | Heartbeat timeout auto-cleanup |

---

## Extensibility

### Plugin Extensions

```go
// Policy Plugin
type PolicyPlugin interface {
    Name() string
    CheckPermission(req *PermissionRequest) (bool, error)
}

// Evidence Backend Plugin
type EvidenceBackend interface {
    Store(record *EvidenceRecord) error
    Query(query *Query) ([]*EvidenceRecord, error)
}
```

---

## License

MIT License - See LICENSE file

---

## Contributing

Welcome to submit Issues and Pull Requests!

---

## Contact

- Project Maintainer: Trusted Data Space Team
- Email: trusted-space@example.com

---

**Note**: Certificates generated by this project are for testing only. Do not use in production environments.
