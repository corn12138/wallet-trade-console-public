// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "./MockERC20.sol";

/**
 * @title MockUSDT
 * @dev 模拟 USDT 代币（6 位小数）
 */
contract MockUSDT is MockERC20 {
    constructor() MockERC20("Mock Tether USD", "USDT", 6) {}
}

/**
 * @title MockUSDC
 * @dev 模拟 USDC 代币（6 位小数）
 */
contract MockUSDC is MockERC20 {
    constructor() MockERC20("Mock USD Coin", "USDC", 6) {}
}

/**
 * @title MockWETH
 * @dev 模拟 WETH 代币（18 位小数）
 * 支持 ETH 存取
 */
contract MockWETH is MockERC20 {
    event Deposit(address indexed dst, uint256 wad);
    event Withdrawal(address indexed src, uint256 wad);

    constructor() MockERC20("Wrapped Ether", "WETH", 18) {}

    /**
     * @dev 存入 ETH，获得 WETH
     */
    function deposit() public payable {
        _mint(msg.sender, msg.value);
        emit Deposit(msg.sender, msg.value);
    }

    /**
     * @dev 取回 ETH，销毁 WETH
     */
    function withdraw(uint256 wad) public {
        require(balanceOf(msg.sender) >= wad, "WETH: insufficient balance");
        _burn(msg.sender, wad);
        payable(msg.sender).transfer(wad);
        emit Withdrawal(msg.sender, wad);
    }

    /**
     * @dev 直接转账 ETH 时自动存入
     */
    receive() external payable {
        deposit();
    }
}

/**
 * @title MockWBTC
 * @dev 模拟 WBTC 代币（8 位小数）
 */
contract MockWBTC is MockERC20 {
    constructor() MockERC20("Wrapped BTC", "WBTC", 8) {}
}

/**
 * @title MockDAI
 * @dev 模拟 DAI 代币（18 位小数）
 */
contract MockDAI is MockERC20 {
    constructor() MockERC20("Dai Stablecoin", "DAI", 18) {}
}
