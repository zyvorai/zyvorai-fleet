// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// DigestPrefix labels the digest algorithm so a future change is unambiguous.
const DigestPrefix = "sha256:"

// ComputeRevisionDigest returns a content digest over the desired state of a
// revision (its ordered workloads). Name, notes, id and timestamps are
// deliberately excluded: two revisions that declare the same workloads have the
// same digest, and renaming never changes what an agent would apply.
//
// The encoding is encoding/json over typed structs, so field order is fixed by
// the struct definition and map keys are sorted; it is stable for a given
// release of this module. The digest detects corruption and accidental
// modification in transit or in a cached copy. It is NOT a signature: anyone
// who can rewrite both the revision and its digest (for example a compromised
// control plane) can still forge a matching pair.
func ComputeRevisionDigest(workloads []RuntimeSpec) string {
	if workloads == nil {
		workloads = []RuntimeSpec{}
	}
	b, err := json.Marshal(workloads)
	if err != nil { // RuntimeSpec contains only marshalable types
		return ""
	}
	sum := sha256.Sum256(b)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// VerifyDigest reports whether the revision carries a digest and whether it
// matches its workloads. A revision without a digest (created before digests
// existed) returns present=false and is not treated as a mismatch.
func (r Revision) VerifyDigest() (present, ok bool) {
	if r.Digest == "" {
		return false, true
	}
	return true, r.Digest == ComputeRevisionDigest(r.Workloads)
}
