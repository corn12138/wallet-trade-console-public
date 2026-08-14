// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IPerpVault {
    function deposit(address token, uint256 amount) external;
    function withdraw(address token, uint256 amount, address to) external;
    function increasePoolAmount(address token, uint256 amount) external;
    function decreasePoolAmount(address token, uint256 amount) external;
    function poolAmounts(address token) external view returns (uint256);
    function reservedAmounts(address token) external view returns (uint256);
    function increaseReservedAmount(address token, uint256 amount) external;
    function decreaseReservedAmount(address token, uint256 amount) external;
}
