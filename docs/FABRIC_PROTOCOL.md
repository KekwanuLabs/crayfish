# Fabric Protocol Specification

## Trust Infrastructure for the Agentic Future

**Version:** 0.1 (Draft)
**Authors:** Identities AI
**Status:** Design Document

---

## Executive Summary

Fabric is a cryptographic trust protocol that enables humans to delegate agency to AI systems in a verifiable, revocable, and quantum-resistant manner.

In the emerging agentic future:
- Humans will have **many AI agents** acting on their behalf
- These agents will attend meetings, send emails, make decisions
- They will look like us (video), sound like us (voice), and move like us (robotics)
- **Trust becomes the fundamental infrastructure**

Fabric answers the question: **"How do I know this agent legitimately represents this human?"**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                                                                             │
│    "In a world where AI can perfectly imitate humans,                       │
│     cryptographic identity becomes the only truth."                         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Table of Contents

1.  [The Problem](#1-the-problem)
2.  [Core Concepts](#2-core-concepts)
3.  [Cryptographic Foundations](#3-cryptographic-foundations)
4.  [Protocol Specification](#4-protocol-specification)
5.  [Blockchain Integration](#5-blockchain-integration)
6.  [Existing Identity Integration](#6-existing-identity-integration)
7.  [Modality Support](#7-modality-support)
8.  [Crayfish Integration](#8-crayfish-integration)
9.  [Security Architecture](#9-security-architecture)
10. [Implementation Roadmap](#10-implementation-roadmap)

---

## 1. The Problem

### 1.1 The Impersonation Crisis

We are entering an era where:

| Capability      | Current State               | Near Future (2026-2027)      |
|-----------------|-----------------------------|------------------------------|
| Voice cloning   | 5 seconds of audio          | Real-time, indistinguishable |
| Video synthesis | Minutes of training         | Real-time avatars            |
| Text generation | Already indistinguishable   | Personality cloning          |
| Robotics        | Primitive                   | Humanoid agents              |

**Detection is a losing game.** Every detection method will eventually be defeated by better synthesis.

**Identity is the only sustainable solution.** You cannot fake a cryptographic signature.

### 1.2 The Agent Explosion

```
                2025                              2028

              ┌─────────┐                    ┌─────────────────┐
              │  Human  │                    │      Human      │
              └────┬────┘                    └────────┬────────┘
                   │                                  │
              ┌────┴────┐               ┌─────────────┼─────────────┐
              │  Phone  │               │      │      │      │      │
              │   App   │               ▼      ▼      ▼      ▼      ▼
              └─────────┘            ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐
                                     │Home│ │Work│ │Car │ │Meet│ │Shop│
                                     │ AI │ │ AI │ │ AI │ │ AI │ │ AI │
                                     └────┘ └────┘ └────┘ └────┘ └────┘

              1 device                        Many agents, one human
```

Every human will have a **constellation of agents**. Each needs to prove it belongs to that human.

### 1.3 Why Current Solutions Fail

| Solution                    | Why It Fails for Agents                  |
|-----------------------------|------------------------------------------|
| OAuth/OIDC                  | Designed for apps, not autonomous actors |
| API Keys                    | No identity, just authorization          |
| SSO                         | Human must be present                    |
| Biometrics                  | Can't prove an agent's legitimacy        |
| Deepfake detection          | Arms race, eventually loses              |

**Fabric fills the gap:** cryptographic proof that an agent is delegated by a verified human.

---

## 2. Core Concepts

### 2.1 The Identity Hierarchy

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         FABRIC IDENTITY HIERARCHY                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│                          ┌─────────────────────┐                            │
│                          │    ANCHOR LAYER     │                            │
│                          │                     │                            │
│                          │  Government ID      │                            │
│                          │  Biometrics (CLEAR) │                            │
│                          │  Enterprise SSO     │                            │
│                          │  DID Documents      │                            │
│                          └──────────┬──────────┘                            │
│                                     │                                       │
│                                     │ binds to                              │
│                                     ▼                                       │
│                          ┌─────────────────────┐                            │
│                          │    HUMAN ROOT       │                            │
│                          │                     │                            │
│                          │  Ed25519 + Dilithium│                            │
│                          │  (quantum-hybrid)   │                            │
│                          │                     │                            │
│                          │  The human's master │                            │
│                          │  signing identity   │                            │
│                          └──────────┬──────────┘                            │
│                                     │                                       │
│                    ┌────────────────┼────────────────┐                      │
│                    │                │                │                      │
│                    ▼                ▼                ▼                      │
│            ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                │
│            │  AGENT KEY  │  │  AGENT KEY  │  │  AGENT KEY  │                │
│            │             │  │             │  │             │                │
│            │  Crayfish   │  │  Zoom Bot   │  │  Email AI   │                │
│            │  (home)     │  │  (meetings) │  │  (work)     │                │
│            └─────────────┘  └─────────────┘  └─────────────┘                │
│                    │                │                │                      │
│                    │                │                │                      │
│            ┌───────┴────┐   ┌───────┴────┐   ┌───────┴────┐                 │
│            │ MODALITIES │   │ MODALITIES │   │ MODALITIES │                 │
│            │            │   │            │   │            │                 │
│            │ • Voice    │   │ • Video    │   │ • Text     │                 │
│            │ • Text     │   │ • Voice    │   │            │                 │
│            └────────────┘   └────────────┘   └────────────┘                 │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Core Primitives

| Primitive                  | Description                                   |
|----------------------------|-----------------------------------------------|
| **Human Root**             | The master identity keypair owned by a human  |
| **Agent Key**              | An identity keypair generated by an agent     |
| **Delegation Certificate** | Signed proof that an agent belongs to a human |
| **Scope**                  | What the agent is allowed to do               |
| **Modality Binding**       | Cryptographic link to voice/video/other       |
| **Proof Bundle**           | What an agent presents to prove identity      |
| **Revocation**             | How to invalidate compromised credentials     |

### 2.3 The Trust Equation

```
Trust = Verify(HumanRoot) + Verify(Delegation) + Verify(Liveness) + Verify(Scope)
```

A verifier checks:
1. **Human Root is legitimate** (anchored to real identity)
2. **Delegation is valid** (signed by Human Root, not expired, not revoked)
3. **Agent is live** (can sign fresh challenges)
4. **Scope permits action** (delegation allows this operation)

---

## 3. Cryptographic Foundations

### 3.1 Quantum-Resistant Design

**Threat:** Quantum computers will break RSA and elliptic curve cryptography.

**Timeline:** Estimates range from 2030-2040 for cryptographically relevant quantum computers.

**Strategy:** Hybrid cryptography — use both classical and post-quantum algorithms.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      QUANTUM-HYBRID SIGNATURE                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Message ──┬──▶ Ed25519.Sign() ──────┬──▶ Combined Signature               │
│             │                         │                                     │
│             └──▶ Dilithium3.Sign() ───┘                                     │
│                                                                             │
│   Verification:                                                             │
│   • Today: Verify Ed25519 (fast, proven)                                    │
│   • Future: Verify Dilithium (quantum-safe)                                 │
│   • Both must pass for full trust                                           │
│                                                                             │
│   Why hybrid?                                                               │
│   • Ed25519 is battle-tested, fast, small signatures                        │
│   • Dilithium is NIST-approved post-quantum, but newer                      │
│   • Hybrid gives security of both                                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Algorithm Choices

| Purpose            | Classical   | Post-Quantum | Notes                    |
|--------------------|-------------|--------------|--------------------------|
| **Signing**        | Ed25519     | Dilithium3   | NIST PQC winner          |
| **Key Exchange**   | X25519      | Kyber768     | For encrypted channels   |
| **Hashing**        | SHA-256     | SHA-3-256    | Both quantum-resistant   |
| **Key Derivation** | HKDF-SHA256 | HKDF-SHA3    | Deterministic derivation |

### 3.3 Key Sizes and Performance

| Algorithm  | Public Key  | Signature   | Sign (Pi4) | Verify (Pi4) |
|------------|-------------|-------------|------------|--------------|
| Ed25519    | 32 bytes    | 64 bytes    | 0.1 ms     | 0.2 ms       |
| Dilithium3 | 1,952 bytes | 3,293 bytes | 1.5 ms     | 0.5 ms       |
| **Hybrid** | ~2 KB       | ~3.4 KB     | ~2 ms      | ~1 ms        |

**Trade-off:** Larger signatures for quantum resistance. Acceptable for identity proofs.

### 3.4 Canonical Serialization

**Critical:** Signatures must be computed over canonical byte representations.

```
Format: CBOR (Concise Binary Object Representation)
Mode: Canonical (deterministic ordering)
Library: github.com/fxamacker/cbor/v2 (Go)

Why CBOR?
• Deterministic encoding
• Compact binary format
• Wide language support
• IETF standard (RFC 8949)
```

---

## 4. Protocol Specification

### 4.1 Data Structures

#### 4.1.1 Human Root Identity

```go
type HumanRoot struct {
    // Unique identifier (derived from public key)
    ID              string    `cbor:"1,keyasint"`

    // Classical public key (Ed25519)
    ClassicalPubKey []byte    `cbor:"2,keyasint"`  // 32 bytes

    // Post-quantum public key (Dilithium3)
    QuantumPubKey   []byte    `cbor:"3,keyasint"`  // 1,952 bytes

    // Creation timestamp
    CreatedAt       int64     `cbor:"4,keyasint"`  // Unix timestamp

    // Optional: DID document reference
    DID             string    `cbor:"5,keyasint,omitempty"`

    // Optional: Anchor attestations (government ID, biometrics)
    Anchors         []Anchor  `cbor:"6,keyasint,omitempty"`
}

type Anchor struct {
    Type        string `cbor:"1,keyasint"` // "government_id", "biometric", "enterprise"
    Provider    string `cbor:"2,keyasint"` // "clear", "okta", "azure_ad"
    Reference   string `cbor:"3,keyasint"` // Opaque reference (privacy-preserving)
    VerifiedAt  int64  `cbor:"4,keyasint"`
    Attestation []byte `cbor:"5,keyasint"` // Provider's signature
}
```

#### 4.1.2 Agent Identity

```go
type AgentIdentity struct {
    // Unique identifier (derived from public key)
    ID              string `cbor:"1,keyasint"`

    // Agent's classical public key
    ClassicalPubKey []byte `cbor:"2,keyasint"`  // 32 bytes

    // Agent's post-quantum public key
    QuantumPubKey   []byte `cbor:"3,keyasint"`  // 1,952 bytes

    // Agent metadata
    Name            string `cbor:"4,keyasint"`  // Human-readable name
    Type            string `cbor:"5,keyasint"`  // "crayfish", "zoom_bot", "email_agent"
    Version         string `cbor:"6,keyasint"`

    // Hardware binding (optional)
    DeviceAttestation []byte `cbor:"7,keyasint,omitempty"`

    // Modality bindings
    Modalities      []ModalityBinding `cbor:"8,keyasint,omitempty"`
}

type ModalityBinding struct {
    Type        string `cbor:"1,keyasint"` // "voice", "video", "biometric"
    ModelHash   []byte `cbor:"2,keyasint"` // SHA-256 of voice/video model
    SampleHash  []byte `cbor:"3,keyasint"` // SHA-256 of original sample
    CreatedAt   int64  `cbor:"4,keyasint"`
}
```

#### 4.1.3 Delegation Certificate

```go
type DelegationCert struct {
    // Certificate metadata
    CertID          string   `cbor:"1,keyasint"`  // Random unique ID
    Version         int      `cbor:"2,keyasint"`  // Protocol version

    // Issuer (the human)
    IssuerID        string   `cbor:"3,keyasint"`  // Human Root ID
    IssuerPubKeys   PubKeys  `cbor:"4,keyasint"`  // Human's public keys

    // Subject (the agent)
    SubjectID       string   `cbor:"5,keyasint"`  // Agent ID
    SubjectPubKeys  PubKeys  `cbor:"6,keyasint"`  // Agent's public keys

    // Delegation parameters
    Scope           []string `cbor:"7,keyasint"`  // Allowed actions
    Audience        []string `cbor:"8,keyasint"`  // Who can rely on this

    // Validity
    IssuedAt        int64    `cbor:"9,keyasint"`
    ExpiresAt       int64    `cbor:"10,keyasint"`

    // Modality permissions
    AllowedModalities []string `cbor:"11,keyasint,omitempty"` // ["voice", "video"]

    // Constraints
    MaxDelegationDepth int   `cbor:"12,keyasint,omitempty"` // Can this agent delegate?

    // Signatures (hybrid)
    ClassicalSig    []byte   `cbor:"13,keyasint"` // Ed25519
    QuantumSig      []byte   `cbor:"14,keyasint"` // Dilithium3
}

type PubKeys struct {
    Classical []byte `cbor:"1,keyasint"`
    Quantum   []byte `cbor:"2,keyasint"`
}
```

#### 4.1.4 Proof Bundle

```go
type ProofBundle struct {
    // Agent identity
    AgentID         string          `cbor:"1,keyasint"`
    AgentPubKeys    PubKeys         `cbor:"2,keyasint"`

    // Delegation chain
    Delegations     []DelegationCert `cbor:"3,keyasint"`

    // Liveness proof
    Challenge       []byte          `cbor:"4,keyasint"`
    ChallengeTime   int64           `cbor:"5,keyasint"`
    ChallengeSig    HybridSig       `cbor:"6,keyasint"`

    // Optional: Modality proof
    ModalityProof   *ModalityProof  `cbor:"7,keyasint,omitempty"`
}

type HybridSig struct {
    Classical []byte `cbor:"1,keyasint"`
    Quantum   []byte `cbor:"2,keyasint"`
}

type ModalityProof struct {
    Type        string `cbor:"1,keyasint"` // "voice", "video"
    ContentHash []byte `cbor:"2,keyasint"` // Hash of content being proven
    ModelHash   []byte `cbor:"3,keyasint"` // Hash of model used
    Watermark   []byte `cbor:"4,keyasint"` // Embedded watermark data
}
```

### 4.2 Operations

#### 4.2.1 Generate Human Root

```
INPUT:  Entropy source, optional anchor attestations
OUTPUT: HumanRoot identity

1. Generate Ed25519 keypair: (classical_priv, classical_pub)
2. Generate Dilithium3 keypair: (quantum_priv, quantum_pub)
3. Derive ID: SHA-256(classical_pub || quantum_pub)[:16] encoded as base58
4. Store private keys in secure enclave if available
5. Create HumanRoot structure
6. Optionally attach anchor attestations
```

#### 4.2.2 Issue Delegation

```
INPUT:  HumanRoot, AgentIdentity, scope, expiry
OUTPUT: DelegationCert

1. Validate agent identity
2. Construct unsigned DelegationCert
3. Serialize to canonical CBOR (excluding signature fields)
4. Sign with Ed25519: classical_sig = Ed25519.Sign(human_priv, cbor_bytes)
5. Sign with Dilithium3: quantum_sig = Dilithium3.Sign(human_quantum_priv, cbor_bytes)
6. Attach signatures to certificate
7. Return complete DelegationCert
```

#### 4.2.3 Create Proof Bundle

```
INPUT:  AgentIdentity, DelegationCert(s), challenge
OUTPUT: ProofBundle

1. Verify challenge freshness (timestamp within acceptable window)
2. Create proof data: challenge || timestamp
3. Sign with agent's Ed25519 key
4. Sign with agent's Dilithium3 key
5. Assemble ProofBundle with delegation chain
6. Optionally attach modality proof
```

#### 4.2.4 Verify Proof Bundle

```
INPUT:  ProofBundle, expected scope, revocation list
OUTPUT: VerifyResult (valid/invalid, human_id, agent_id, scope)

1. VERIFY CHAIN:
   a. For each delegation in chain:
      - Verify classical signature (Ed25519)
      - Verify quantum signature (Dilithium3)
      - Check expiry
      - Check revocation status
      - Verify issuer matches previous subject (for chains)
   b. Verify root issuer is a valid HumanRoot

2. VERIFY LIVENESS:
   a. Check challenge timestamp is recent (< 5 minutes)
   b. Verify classical challenge signature
   c. Verify quantum challenge signature

3. VERIFY SCOPE:
   a. Check delegation scope includes requested action
   b. Check audience includes verifier

4. VERIFY MODALITY (if present):
   a. Verify content hash matches actual content
   b. Verify model hash is in delegation's allowed modalities
   c. Verify watermark if required

5. RETURN: VerifyResult with extracted claims
```

### 4.3 Revocation

#### 4.3.1 Revocation List

```go
type RevocationList struct {
    IssuerID    string   `cbor:"1,keyasint"` // Human Root ID
    UpdatedAt   int64    `cbor:"2,keyasint"`
    RevokedCerts []string `cbor:"3,keyasint"` // List of CertIDs

    // Signature by Human Root
    ClassicalSig []byte  `cbor:"4,keyasint"`
    QuantumSig   []byte  `cbor:"5,keyasint"`
}
```

#### 4.3.2 Revocation Strategies

| Strategy              | Pros                     | Cons                    | Use Case      |
|-----------------------|--------------------------|-------------------------|---------------|
| **Short-lived certs** | No revocation needed     | Frequent renewal        | High-security |
| **Revocation list**   | Simple                   | Must fetch/cache        | General use   |
| **OCSP-style**        | Real-time                | Availability dependency | Enterprise    |
| **Blockchain**        | Immutable, decentralized | Slow, costs             | High-value    |

**Recommendation:** Short-lived (24-72h) + revocation list + blockchain for audit trail.

---

## 5. Blockchain Integration

### 5.1 When to Use Blockchain

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     BLOCKCHAIN DECISION MATRIX                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   USE BLOCKCHAIN FOR:                 DO NOT USE BLOCKCHAIN FOR:            │
│   ───────────────────                 ─────────────────────────             │
│                                                                             │
│   ✓ Human Root registration           ✗ Real-time verification              │
│   ✓ Revocation announcements          ✗ Delegation storage                  │
│   ✓ Audit trail of delegations        ✗ Challenge-response                  │
│   ✓ Dispute resolution evidence       ✗ High-frequency operations           │
│   ✓ Decentralized trust anchor        ✗ Anything latency-sensitive          │
│                                                                             │
│   Blockchain = audit log, not live infrastructure                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 5.2 Hybrid Architecture

```
┌────────────────────────────────────────────────────────────────────────────┐
│                      FABRIC HYBRID ARCHITECTURE                            │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│   LAYER 1: REAL-TIME (Off-Chain)                                           │
│   ──────────────────────────────                                           │
│   • Delegation issuance (instant)                                          │
│   • Proof verification (milliseconds)                                      │
│   • Challenge-response (live)                                              │
│   • Revocation checking (cached)                                           │
│                                                                            │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │  Agent ◀──▶ Verifier ◀──▶ Revocation Cache ◀──▶ Policy Engine       │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                      │                                     │
│                                      │ async sync                          │
│                                      ▼                                     │
│   LAYER 2: SETTLEMENT (On-Chain)                                           │
│   ──────────────────────────────                                           │
│   • Human Root registration (once)                                         │
│   • Revocation announcements (rare)                                        │
│   • Delegation audit logs (async)                                          │
│   • Dispute evidence (when needed)                                         │
│                                                                            │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │  Ethereum/Polygon ◀──▶ Fabric Registry Contract                     │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

### 5.3 Smart Contract Design

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

/**
 * @title FabricRegistry
 * @notice Anchors Human Root identities and tracks revocations
 */
contract FabricRegistry {

    struct HumanRootAnchor {
        bytes32 rootId;           // SHA-256 of public keys
        bytes classicalPubKey;    // Ed25519 (32 bytes)
        bytes quantumPubKeyHash;  // SHA-256 of Dilithium key (save gas)
        uint256 registeredAt;
        bool revoked;
    }

    struct RevocationEvent {
        bytes32 certId;
        bytes32 issuerId;
        uint256 revokedAt;
        bytes32 reason;           // Hash of reason string
    }

    mapping(bytes32 => HumanRootAnchor) public roots;
    mapping(bytes32 => RevocationEvent) public revocations;

    event RootRegistered(bytes32 indexed rootId, uint256 timestamp);
    event RootRevoked(bytes32 indexed rootId, uint256 timestamp);
    event CertRevoked(bytes32 indexed certId, bytes32 indexed issuerId, uint256 timestamp);

    /**
     * @notice Register a new Human Root identity
     */
    function registerRoot(
        bytes32 rootId,
        bytes calldata classicalPubKey,
        bytes32 quantumPubKeyHash
    ) external {
        require(roots[rootId].registeredAt == 0, "Already registered");
        require(classicalPubKey.length == 32, "Invalid key length");

        roots[rootId] = HumanRootAnchor({
            rootId: rootId,
            classicalPubKey: classicalPubKey,
            quantumPubKeyHash: quantumPubKeyHash,
            registeredAt: block.timestamp,
            revoked: false
        });

        emit RootRegistered(rootId, block.timestamp);
    }

    /**
     * @notice Revoke a delegation certificate
     * @dev Caller must prove ownership via signature (verified off-chain)
     */
    function revokeCert(
        bytes32 certId,
        bytes32 issuerId,
        bytes32 reason,
        bytes calldata signature  // Verified off-chain or via ecrecover
    ) external {
        require(roots[issuerId].registeredAt > 0, "Unknown issuer");
        require(!roots[issuerId].revoked, "Issuer revoked");

        revocations[certId] = RevocationEvent({
            certId: certId,
            issuerId: issuerId,
            revokedAt: block.timestamp,
            reason: reason
        });

        emit CertRevoked(certId, issuerId, block.timestamp);
    }

    /**
     * @notice Check if a certificate is revoked
     */
    function isRevoked(bytes32 certId) external view returns (bool) {
        return revocations[certId].revokedAt > 0;
    }
}
```

### 5.4 Chain Selection

| Chain                  | Pros                       | Cons               | Recommendation              |
|------------------------|----------------------------|--------------------|-----------------------------|
| **Ethereum L1**        | Most secure, decentralized | Expensive, slow    | Root registration only      |
| **Polygon**            | Cheap, fast, EVM           | Less decentralized | Revocations, audit logs     |
| **Arbitrum/Optimism**  | Cheap, Ethereum security   | Sequencer risk     | Good alternative to Polygon |
| **Solana**             | Very fast                  | Different tooling  | If non-EVM preferred        |
| **Custom L2**          | Full control               | Must maintain      | Long-term option            |

**Recommendation:** Start with Polygon for all operations, optionally anchor critical roots to Ethereum L1.

---

## 6. Existing Identity Integration

### 6.1 DID (Decentralized Identifiers)

Fabric Human Roots can be represented as DIDs:

```
did:fabric:human:5K7xGbQ9z...  →  Resolves to Human Root public keys
did:fabric:agent:3nRtY2mP1...  →  Resolves to Agent Identity
```

**DID Document:**

```json
{
  "@context": ["https://www.w3.org/ns/did/v1", "https://fabric.identities.ai/v1"],
  "id": "did:fabric:human:5K7xGbQ9z...",
  "verificationMethod": [
    {
      "id": "did:fabric:human:5K7xGbQ9z...#classical",
      "type": "Ed25519VerificationKey2020",
      "controller": "did:fabric:human:5K7xGbQ9z...",
      "publicKeyMultibase": "z6Mkf..."
    },
    {
      "id": "did:fabric:human:5K7xGbQ9z...#quantum",
      "type": "Dilithium3VerificationKey2024",
      "controller": "did:fabric:human:5K7xGbQ9z...",
      "publicKeyMultibase": "zDil..."
    }
  ],
  "authentication": ["#classical", "#quantum"],
  "service": [{
    "id": "#fabric-anchor",
    "type": "FabricAnchor",
    "serviceEndpoint": "https://registry.identities.ai/roots/5K7xGbQ9z..."
  }]
}
```

### 6.2 Government Identity (CLEAR, etc.)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                   GOVERNMENT ID ANCHOR FLOW                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   1. Human verifies with CLEAR (biometrics, ID scan)                        │
│                                                                             │
│   2. CLEAR issues attestation:                                              │
│      {                                                                      │
│        "provider": "clear.me",                                              │
│        "verified_at": "2025-01-15T10:30:00Z",                               │
│        "level": "identity_verified",                                        │
│        "reference": "opaque_reference_12345",  // Privacy-preserving        │
│        "signature": "..."                                                   │
│      }                                                                      │
│                                                                             │
│   3. Human attaches attestation to their Fabric Root                        │
│                                                                             │
│   4. Verifiers can request government-anchored roots                        │
│      "I only trust agents whose humans have CLEAR verification"             │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 6.3 Enterprise Identity (Okta, Azure AD)

```go
type EnterpriseAnchor struct {
    Provider      string `cbor:"1,keyasint"` // "okta", "azure_ad", "google"
    TenantID      string `cbor:"2,keyasint"` // Organization identifier
    UserReference string `cbor:"3,keyasint"` // Opaque user reference
    Claims        []string `cbor:"4,keyasint"` // ["employee", "department:engineering"]
    Attestation   []byte `cbor:"5,keyasint"` // SAML assertion or JWT
    VerifiedAt    int64  `cbor:"6,keyasint"`
}
```

**Flow:**
1. User authenticates via enterprise SSO
2. Enterprise issues signed attestation to Fabric
3. Fabric Root gains "enterprise-verified" status
4. Enterprise policies can require this anchor

### 6.4 Anchor Levels

| Level              | Anchors Required      | Use Case               |
|--------------------|-----------------------|------------------------|
| **Self-declared**  | None                  | Personal use, low-risk |
| **Email-verified** | Email verification    | Basic trust            |
| **Enterprise**     | SSO attestation       | Business use           |
| **Government**     | CLEAR/ID verification | High-value, regulated  |
| **Multi-anchor**   | 2+ of above           | Maximum trust          |

---

## 7. Modality Support

### 7.1 Voice Identity

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          VOICE BINDING                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ENROLLMENT:                                                               │
│   ───────────                                                               │
│   1. Human records 30+ seconds of speech                                    │
│   2. Voice Forge trains a synthesis model (Piper-compatible)                │
│   3. Hashes recorded:                                                       │
│      • voice_sample_hash = SHA-256(original_recording)                      │
│      • model_hash = SHA-256(trained_model.onnx)                             │
│   4. Human signs ModalityBinding                                            │
│   5. Binding attached to Agent Identity                                     │
│                                                                             │
│   VERIFICATION:                                                             │
│   ─────────────                                                             │
│   1. Agent produces speech                                                  │
│   2. Agent includes ModalityProof:                                          │
│      • content_hash = SHA-256(audio_output)                                 │
│      • model_hash = (matches enrollment)                                    │
│      • watermark = (Resemble AI PerTh or similar)                           │
│   3. Verifier checks:                                                       │
│      • model_hash is in delegation's allowed modalities                     │
│      • watermark verifies (proves this model generated this audio)          │
│                                                                             │
│   RESULT:                                                                   │
│   ───────                                                                   │
│   "This voice was generated by an agent delegated by Human X,               │
│    using a voice model trained from Human X's voice."                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 7.2 Video Identity (Avatars)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          VIDEO BINDING                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ENROLLMENT:                                                               │
│   ───────────                                                               │
│   1. Human records video (face, expressions, movements)                     │
│   2. Avatar Forge trains a video synthesis model                            │
│   3. Hashes recorded:                                                       │
│      • video_sample_hash = SHA-256(original_video)                          │
│      • model_hash = SHA-256(avatar_model)                                   │
│   4. Human signs ModalityBinding with scope ["video", "avatar"]             │
│                                                                             │
│   LIVE USE:                                                                 │
│   ─────────                                                                 │
│   1. Agent joins video call as avatar                                       │
│   2. Avatar visually indicates "AI representative" (subtle badge)           │
│   3. Agent can prove delegation if challenged                               │
│   4. Deepfake detection runs as secondary verification                      │
│                                                                             │
│   POLICY OPTIONS:                                                           │
│   ───────────────                                                           │
│   • "Accept verified AI avatars" → Check Fabric proof                       │
│   • "Humans only" → Reject agents even with proof                           │
│   • "Flagged mode" → Accept but display "AI representative" label           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 7.3 Robotics

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       ROBOTICS BINDING                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ENROLLMENT:                                                               │
│   ───────────                                                               │
│   1. Robot generates AgentIdentity with hardware attestation                │
│   2. Hardware attestation proves:                                           │
│      • Specific robot (serial number, TPM)                                  │
│      • Running verified firmware                                            │
│   3. Human delegates to this specific robot                                 │
│   4. Delegation includes physical capabilities scope                        │
│                                                                             │
│   SCOPES FOR ROBOTICS:                                                      │
│   ────────────────────                                                      │
│   • "navigate:indoor" - Can move within buildings                           │
│   • "manipulate:objects" - Can pick up/move objects                         │
│   • "interact:humans" - Can physically interact                             │
│   • "operate:vehicle" - Can drive                                           │
│                                                                             │
│   VERIFICATION:                                                             │
│   ─────────────                                                             │
│   When robot interacts with system:                                         │
│   1. Robot presents ProofBundle + hardware attestation                      │
│   2. System verifies delegation chain                                       │
│   3. System checks scope permits the action                                 │
│   4. Action proceeds or is denied                                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 8. Crayfish Integration

### 8.1 Architecture

```
┌────────────────────────────────────────────────────────────────────────────┐
│                         CRAYFISH + FABRIC                                  │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│   CRAYFISH (Raspberry Pi)                                                  │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                     │  │
│   │  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐            │  │
│   │  │   Gateway     │  │   Runtime     │  │    Voice      │            │  │
│   │  │   (HTTP)      │  │   (Agent)     │  │   (Piper)     │            │  │
│   │  └───────┬───────┘  └───────┬───────┘  └───────┬───────┘            │  │
│   │          │                  │                  │                    │  │
│   │          └──────────────────┼──────────────────┘                    │  │
│   │                             │                                       │  │
│   │                    ┌────────┴────────┐                              │  │
│   │                    │     FABRIC      │                              │  │
│   │                    │     MODULE      │                              │  │
│   │                    │                 │                              │  │
│   │                    │  • AgentIdentity│                              │  │
│   │                    │  • Delegation   │                              │  │
│   │                    │  • ProofBundle  │                              │  │
│   │                    │  • VoiceBinding │                              │  │
│   │                    └────────┬────────┘                              │  │
│   │                             │                                       │  │
│   └─────────────────────────────┼───────────────────────────────────────┘  │
│                                 │                                          │
│                                 │ HTTPS                                    │
│                                 ▼                                          │
│   IDENTITIES AI (Cloud)                                                    │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │  • Verification API                                                 │  │
│   │  • Revocation service                                               │  │
│   │  • Voice Forge (training)                                           │  │
│   │  • Blockchain anchor                                                │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

### 8.2 Crayfish Fabric Module

```go
// internal/fabric/fabric.go

package fabric

import (
    "crypto/ed25519"
    "crypto/sha256"
    "encoding/base64"
    "os"
    "path/filepath"
    "time"

    "github.com/cloudflare/circl/sign/dilithium/mode3"
)

// Module is Crayfish's Fabric integration
type Module struct {
    identity    *AgentIdentity
    delegation  *DelegationCert
    voiceBinding *ModalityBinding
    keyPath     string
    logger      *slog.Logger
}

// New creates a Fabric module, generating identity on first run
func New(dataDir string, logger *slog.Logger) (*Module, error) {
    m := &Module{
        keyPath: filepath.Join(dataDir, "fabric"),
        logger:  logger,
    }

    if err := os.MkdirAll(m.keyPath, 0700); err != nil {
        return nil, err
    }

    // Load or generate identity
    if m.identityExists() {
        if err := m.loadIdentity(); err != nil {
            return nil, err
        }
    } else {
        if err := m.generateIdentity(); err != nil {
            return nil, err
        }
    }

    // Load delegation if exists
    m.loadDelegation() // Non-fatal if missing

    return m, nil
}

// generateIdentity creates new agent keypairs
func (m *Module) generateIdentity() error {
    // Ed25519 (classical)
    classPub, classPriv, err := ed25519.GenerateKey(nil)
    if err != nil {
        return err
    }

    // Dilithium3 (quantum)
    quantumPub, quantumPriv, err := mode3.GenerateKey(nil)
    if err != nil {
        return err
    }

    // Derive ID
    idBytes := sha256.Sum256(append(classPub, quantumPub.Bytes()...))
    id := base64.RawURLEncoding.EncodeToString(idBytes[:16])

    m.identity = &AgentIdentity{
        ID:              id,
        ClassicalPubKey: classPub,
        QuantumPubKey:   quantumPub.Bytes(),
        Name:            "", // Set during pairing
        Type:            "crayfish",
        CreatedAt:       time.Now().Unix(),
    }

    // Save keys securely
    return m.saveKeys(classPriv, quantumPriv)
}

// AcceptDelegation validates and stores a delegation certificate
func (m *Module) AcceptDelegation(cert *DelegationCert) error {
    // Verify the cert is for this agent
    if !bytes.Equal(cert.SubjectPubKeys.Classical, m.identity.ClassicalPubKey) {
        return errors.New("delegation is not for this agent")
    }

    // Verify signatures
    if err := m.verifyDelegation(cert); err != nil {
        return err
    }

    // Check expiry
    if time.Now().Unix() > cert.ExpiresAt {
        return errors.New("delegation has expired")
    }

    m.delegation = cert
    return m.saveDelegation()
}

// Prove creates a proof bundle for the given challenge
func (m *Module) Prove(challenge []byte) (*ProofBundle, error) {
    if m.delegation == nil {
        return nil, errors.New("not delegated - pairing required")
    }

    // Check delegation still valid
    if time.Now().Unix() > m.delegation.ExpiresAt {
        return nil, errors.New("delegation expired")
    }

    timestamp := time.Now().Unix()
    signData := append(challenge, []byte(fmt.Sprintf("%d", timestamp))...)

    // Sign with both keys
    classSig := ed25519.Sign(m.classicalPriv, signData)
    quantumSig := mode3.Sign(m.quantumPriv, signData)

    return &ProofBundle{
        AgentID: m.identity.ID,
        AgentPubKeys: PubKeys{
            Classical: m.identity.ClassicalPubKey,
            Quantum:   m.identity.QuantumPubKey,
        },
        Delegations:   []DelegationCert{*m.delegation},
        Challenge:     challenge,
        ChallengeTime: timestamp,
        ChallengeSig: HybridSig{
            Classical: classSig,
            Quantum:   quantumSig,
        },
    }, nil
}

// IsPaired returns whether this agent has a valid delegation
func (m *Module) IsPaired() bool {
    return m.delegation != nil && time.Now().Unix() < m.delegation.ExpiresAt
}

// PairingCode returns a code for the human to scan/enter
func (m *Module) PairingCode() string {
    // Compact representation for QR or manual entry
    data := struct {
        ID  string `json:"id"`
        Pub string `json:"pub"` // Just classical for QR size
    }{
        ID:  m.identity.ID,
        Pub: base64.RawURLEncoding.EncodeToString(m.identity.ClassicalPubKey),
    }
    json, _ := json.Marshal(data)
    return base64.RawURLEncoding.EncodeToString(json)
}
```

### 8.3 Setup Wizard Integration

```go
// In setup wizard, add pairing step

// Step: Pair with Human Identity
type PairingStep struct {
    fabric *fabric.Module
}

func (s *PairingStep) Render() template.HTML {
    return `
    <div class="card">
        <h2>Connect Your Identity</h2>
        <p>Your Crayfish needs to know who you are.</p>

        <div class="pairing-options">
            <div class="option">
                <h3>Option 1: Scan QR Code</h3>
                <p>Use the Identities AI app to scan:</p>
                <div id="pairing-qr"></div>
            </div>

            <div class="option">
                <h3>Option 2: Enter Pairing Code</h3>
                <p>Or enter this code in the app:</p>
                <code id="pairing-code">{{.PairingCode}}</code>
            </div>

            <div class="option">
                <h3>Option 3: CLI Pairing</h3>
                <pre>fabric delegate --agent {{.AgentID}} --scope "telegram,email,voice"</pre>
            </div>
        </div>

        <div id="pairing-status">Waiting for delegation...</div>
    </div>
    `
}
```

### 8.4 Voice Integration

```go
// internal/fabric/voice.go

// BindVoice creates a voice modality binding
func (m *Module) BindVoice(samplePath, modelPath string) error {
    // Hash the original voice sample
    sampleData, err := os.ReadFile(samplePath)
    if err != nil {
        return err
    }
    sampleHash := sha256.Sum256(sampleData)

    // Hash the trained model
    modelData, err := os.ReadFile(modelPath)
    if err != nil {
        return err
    }
    modelHash := sha256.Sum256(modelData)

    m.voiceBinding = &ModalityBinding{
        Type:       "voice",
        ModelHash:  modelHash[:],
        SampleHash: sampleHash[:],
        CreatedAt:  time.Now().Unix(),
    }

    // This binding should be signed by the human during pairing
    // For now, store it and request signature
    return m.saveVoiceBinding()
}

// VoiceProof creates a proof for synthesized audio
func (m *Module) VoiceProof(audioData []byte) (*ModalityProof, error) {
    if m.voiceBinding == nil {
        return nil, errors.New("voice not bound")
    }

    contentHash := sha256.Sum256(audioData)

    return &ModalityProof{
        Type:        "voice",
        ContentHash: contentHash[:],
        ModelHash:   m.voiceBinding.ModelHash,
        // Watermark would be extracted from audio
    }, nil
}
```

### 8.5 CLI Commands

```bash
# Show Crayfish identity
$ crayfish fabric id
Agent ID:     5K7xGbQ9z8mNp2...
Type:         crayfish
Created:      2025-02-21T10:30:00Z
Paired:       Yes
Human:        did:fabric:human:3nRtY2mP1...
Expires:      2025-02-28T10:30:00Z
Scopes:       telegram, email.read, email.send, voice
Voice Bound:  Yes (model: en_US-ada-custom)

# Show pairing code
$ crayfish fabric pair
Scan this QR code with the Identities AI app:
[QR CODE]

Or enter this code: CRAY-5K7X-GB9Z-8MNP

# Accept delegation (from file or stdin)
$ crayfish fabric accept < delegation.cbor
Delegation accepted from did:fabric:human:3nRtY2mP1...
Scopes: telegram, email.read, email.send, voice
Expires: 2025-02-28T10:30:00Z

# Prove identity (for testing/debugging)
$ crayfish fabric prove --challenge "test123"
{
  "agent_id": "5K7xGbQ9z8mNp2...",
  "delegation": {...},
  "challenge_sig": {...}
}

# Check revocation status
$ crayfish fabric status
Status: VALID
Last checked: 2 minutes ago
Next renewal: 6 days
```

---

## 9. Security Architecture

### 9.1 Threat Model

| Threat                  | Mitigation                     | Residual Risk                 |
|-------------------------|--------------------------------|-------------------------------|
| **Agent impersonation** | Cryptographic identity         | Key compromise                |
| **Delegation forgery**  | Hybrid signatures              | Quantum computers (mitigated) |
| **Replay attacks**      | Fresh challenges + timestamps  | Clock skew                    |
| **Key theft (agent)**   | Short-lived certs + revocation | Window before revocation      |
| **Key theft (human)**   | Hardware keys + recovery       | Social engineering            |
| **Scope escalation**    | Strict scope checking          | Policy bugs                   |
| **Model theft (voice)** | Watermarking + binding         | Determined attacker           |
| **Quantum attacks**     | Dilithium3                     | Algorithm weakness            |

### 9.2 Key Storage

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         KEY STORAGE BY DEVICE                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   HUMAN ROOT KEYS:                                                          │
│   ────────────────                                                          │
│   Best:     Hardware security key (YubiKey 5)                               │
│   Good:     Phone secure enclave (iOS Keychain, Android Keystore)           │
│   Fallback: Encrypted file with strong passphrase                           │
│                                                                             │
│   AGENT KEYS (Crayfish):                                                    │
│   ───────────────────────                                                   │
│   Best:     TPM 2.0 (if available)                                          │
│   Good:     Encrypted file with device-specific key                         │
│   Pi:       /var/lib/crayfish/fabric/keys (0600, encrypted at rest)         │
│                                                                             │
│   KEY DERIVATION:                                                           │
│   ───────────────                                                           │
│   • Device key derived from: hardware ID + install time + entropy           │
│   • Never stored, regenerated at runtime                                    │
│   • Keys encrypted with AES-256-GCM using derived key                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 9.3 Quantum Resistance Timeline

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      QUANTUM RESISTANCE STRATEGY                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   TODAY (2025):                                                             │
│   • Hybrid: Ed25519 + Dilithium3                                            │
│   • Both signatures required for full trust                                 │
│   • Ed25519 for speed, Dilithium for future-proofing                        │
│                                                                             │
│   TRANSITION (2028-2030):                                                   │
│   • Monitor NIST guidance                                                   │
│   • Potentially add/switch post-quantum algorithms                          │
│   • Certificate versioning allows smooth transition                         │
│                                                                             │
│   QUANTUM ERA (2035+):                                                      │
│   • Phase out classical-only verification                                   │
│   • Post-quantum becomes primary                                            │
│   • Legacy certificates can be re-signed                                    │
│                                                                             │
│   KEY INSIGHT:                                                              │
│   Fabric certificates are short-lived (days to weeks).                      │
│   Even if quantum breaks Ed25519, current certs expire quickly.             │
│   New certs use updated algorithms. No "harvest now, decrypt later" risk.   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 9.4 Defense in Depth

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         DEFENSE LAYERS                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   LAYER 1: CRYPTOGRAPHIC IDENTITY (Fabric)                                  │
│   ─────────────────────────────────────────                                 │
│   • Every agent has verifiable identity                                     │
│   • Delegations are cryptographically bound                                 │
│   • Cannot be forged or spoofed                                             │
│                                                                             │
│   LAYER 2: SCOPE ENFORCEMENT (Fabric)                                       │
│   ────────────────────────────────────                                      │
│   • Delegations have explicit scopes                                        │
│   • Actions checked against scope                                           │
│   • Principle of least privilege                                            │
│                                                                             │
│   LAYER 3: ANOMALY DETECTION (Identities AI)                                │
│   ──────────────────────────────────────────                                │
│   • Behavioral analysis                                                     │
│   • Unusual patterns flagged                                                │
│   • Even valid proofs can be suspicious                                     │
│                                                                             │
│   LAYER 4: DEEPFAKE DETECTION (Identities AI)                               │
│   ───────────────────────────────────────────                               │
│   • For unverified interactions                                             │
│   • Fallback when Fabric proof unavailable                                  │
│   • Risk scoring                                                            │
│                                                                             │
│   LAYER 5: HUMAN OVERSIGHT                                                  │
│   ────────────────────────                                                  │
│   • High-risk actions require human confirmation                            │
│   • Audit logs always available                                             │
│   • Revocation is instant                                                   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 10. Implementation Roadmap

### 10.1 Phase 1: Foundation (Months 1-2)

**Deliverables:**
- [ ] `fabric-go` library (core protocol)
- [ ] Crayfish Fabric module (identity generation, pairing)
- [ ] CLI tools for human root management
- [ ] Basic verification service

**Crayfish changes:**
```
internal/fabric/
├── identity.go      # Agent identity generation
├── delegation.go    # Accept/store delegations
├── proof.go         # Create proof bundles
├── keys.go          # Key storage
└── client.go        # Talk to Identities AI
```

### 10.2 Phase 2: Voice (Months 2-3)

**Deliverables:**
- [ ] Voice recording in setup wizard
- [ ] Voice Forge service (training)
- [ ] Voice binding integration
- [ ] Modality proofs

### 10.3 Phase 3: Blockchain Anchor (Months 3-4)

**Deliverables:**
- [ ] FabricRegistry smart contract
- [ ] Polygon deployment
- [ ] Root registration flow
- [ ] Revocation sync

### 10.4 Phase 4: Enterprise (Months 4-6)

**Deliverables:**
- [ ] Enterprise anchor integration (Okta, Azure AD)
- [ ] Policy engine
- [ ] Compliance reporting
- [ ] SLA-backed verification service

### 10.5 Phase 5: Ecosystem (Months 6-12)

**Deliverables:**
- [ ] Video modality support
- [ ] Phone app for human root
- [ ] Additional language SDKs
- [ ] Third-party integrations
- [ ] Robotics bindings

---

## Appendix A: Wire Formats

### A.1 CBOR Encoding

All Fabric messages use CBOR with canonical encoding (RFC 8949, Section 4.2).

```go
import "github.com/fxamacker/cbor/v2"

var cborEncoder = cbor.EncMode{
    Sort: cbor.SortCanonical,
}.EncMode()
```

### A.2 API Endpoints

```
POST /v1/challenge
  → { "challenge": "<base64>", "expires": <unix_timestamp> }

POST /v1/verify
  ← { "proof_bundle": "<base64-cbor>" }
  → { "valid": true, "human_id": "...", "agent_id": "...", "scope": [...] }

GET /v1/revocations/{human_id}
  → { "revoked_certs": [...], "updated_at": <unix_timestamp> }

POST /v1/anchor/register
  ← { "human_root": "<base64-cbor>", "chain": "polygon" }
  → { "tx_hash": "0x...", "block": 12345 }
```

---

## Appendix B: Example Flows

### B.1 Crayfish Joins Telegram Group

```
1. User adds Crayfish to Telegram group
2. Group admin's bot requests proof:
   Admin Bot → Crayfish: "Prove identity, challenge: abc123"
3. Crayfish creates ProofBundle, signs challenge
4. Crayfish → Admin Bot: ProofBundle
5. Admin Bot → Identities AI: Verify(ProofBundle)
6. Identities AI checks chain, returns:
   { valid: true, human: "Alice", scope: ["telegram"] }
7. Admin Bot: "Verified: Alice's AI assistant has joined"
```

### B.2 AI Avatar Joins Zoom

```
1. AI Avatar attempts to join Zoom meeting
2. Identities AI Zoom agent intercepts
3. Zoom Agent → Avatar: "Present Fabric proof"
4. Avatar presents ProofBundle + VideoModalityProof
5. Zoom Agent verifies:
   - Delegation chain valid
   - Video modality permitted
   - Model hash matches
6. Zoom Agent: Display "AI Representative: Alice" badge
7. Meeting proceeds with transparency
```

---

## Appendix C: Glossary

| Term             | Definition                                                        |
|------------------|-------------------------------------------------------------------|
| **Anchor**       | A binding to an external identity system (government, enterprise) |
| **Delegation**   | A signed certificate granting an agent permission to act          |
| **Fabric**       | The overall trust protocol                                        |
| **Human Root**   | A human's master identity keypair                                 |
| **Modality**     | A form of interaction (voice, video, text, physical)              |
| **Proof Bundle** | The complete evidence an agent presents for verification          |
| **Scope**        | The set of actions a delegation permits                           |
| **Verifier**     | A service that checks proof bundles                               |

---

*Document version: 0.1-draft*
*Last updated: 2025-02-21*
*© Identities AI*
