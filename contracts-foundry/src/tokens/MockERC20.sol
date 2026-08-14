// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/token/ERC20/extensions/ERC20Burnable.sol";
import "@openzeppelin/contracts/token/ERC20/extensions/ERC20Permit.sol";
import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title MockERC20
 * @dev 用于测试的 ERC20 代币，支持任意铸造
 *
 * 特性：
 * - 任何人都可以 mint（仅用于测试）
 * - 支持 burn
 * - 支持 EIP-2612 permit（无 gas 授权）
 *
 * @notice 仅用于测试网络，不要在主网使用
 */
contract MockERC20 is ERC20, ERC20Burnable, ERC20Permit, Ownable {
    uint8 private immutable _decimals;

    /**
     * @dev 构造函数
     * @param name_ 代币名称
     * @param symbol_ 代币符号
     * @param decimals_ 小数位数（通常为 18）
     */
    constructor(
        string memory name_,
        string memory symbol_,
        uint8 decimals_
    ) ERC20(name_, symbol_) ERC20Permit(name_) Ownable(msg.sender) {
        _decimals = decimals_;
    }

    /**
     * @dev 返回代币的小数位数
     */
    function decimals() public view virtual override returns (uint8) {
        return _decimals;
    }

    /**
     * @dev 任何人都可以铸造代币（测试用途）
     * @param to 接收地址
     * @param amount 铸造数量
     */
    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    /**
     * @dev 批量铸造给多个地址
     * @param recipients 接收地址数组
     * @param amounts 对应的数量数组
     */
    function batchMint(address[] calldata recipients, uint256[] calldata amounts) external {
        require(recipients.length == amounts.length, "MockERC20: length mismatch");

        for (uint256 i = 0; i < recipients.length; i++) {
            _mint(recipients[i], amounts[i]);
        }
    }

    /**
     * @dev 便捷函数：给调用者铸造代币
     * @param amount 铸造数量
     */
    function faucet(uint256 amount) external {
        _mint(msg.sender, amount);
    }
}
