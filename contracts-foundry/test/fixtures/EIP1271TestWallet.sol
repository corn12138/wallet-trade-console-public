// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title EIP1271TestWallet
/// @notice Test-only smart-account stand-in for the Go SIWE verifier's EIP-1271
/// path. It is **not** part of the product: it is never deployed by
/// `script/Deploy.s.sol`, never referenced by the deployment registry, and
/// lives under `test/` precisely so the Foundry↔Hardhat source-parity guard
/// (which mirrors `src/` only) ignores it.
///
/// It implements the minimum a Safe-style account needs for
/// `scripts/acceptance/wp1a-auth-local.sh` to prove the success and the
/// rejection branch against a real chain: one owner, one recovery, and the
/// magic value only on an exact match.
contract EIP1271TestWallet {
    /// bytes4(keccak256("isValidSignature(bytes32,bytes)"))
    bytes4 internal constant MAGIC_VALUE = 0x1626ba7e;

    address public immutable owner;

    constructor(address owner_) {
        require(owner_ != address(0), "owner=0");
        owner = owner_;
    }

    /// @notice Returns the EIP-1271 magic value iff `signature` is `owner`'s
    /// secp256k1 signature over `hash`.
    /// @dev Anything else returns `0xffffffff` rather than reverting, so the Go
    /// verifier's "the node answered and the answer was no" branch is exercised
    /// by a non-magic return as well as by a revert.
    function isValidSignature(
        bytes32 hash,
        bytes calldata signature
    )
        external
        view
        returns (bytes4)
    {
        if (signature.length != 65) {
            return 0xffffffff;
        }
        bytes32 r;
        bytes32 s;
        uint8 v;
        assembly ("memory-safe") {
            r := calldataload(signature.offset)
            s := calldataload(add(signature.offset, 32))
            v := byte(0, calldataload(add(signature.offset, 64)))
        }
        if (v < 27) {
            v += 27;
        }
        // Reject the malleable upper half-order s, as a real account would.
        if (uint256(s) > 0x7FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF5D576E7357A4501DDFE92F46681B20A0) {
            return 0xffffffff;
        }
        address recovered = ecrecover(hash, v, r, s);
        if (recovered == address(0) || recovered != owner) {
            return 0xffffffff;
        }
        return MAGIC_VALUE;
    }
}
