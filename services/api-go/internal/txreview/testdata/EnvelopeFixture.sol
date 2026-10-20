// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

// The caller and value checks deliberately fail under a to/data-only eth_call.
// This fixture validates the real EVM boundary without touching protocol funds.
contract EnvelopeFixture {
    function allowance(address, address) external pure returns (uint256) {
        return 42;
    }

    function verify(address expectedSender, uint256 expectedValue) external payable returns (uint256) {
        require(msg.sender == expectedSender, "sender-mismatch");
        require(msg.value == expectedValue, "value-mismatch");
        return msg.value;
    }

    function noReturn() external payable {}

    function forceRevert() external pure {
        revert("fixture-revert");
    }
}
