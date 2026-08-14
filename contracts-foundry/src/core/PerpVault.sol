// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import "../interfaces/IPerpVault.sol";

contract PerpVault is IPerpVault, Ownable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    address public market;

    // Tracks total pooled tokens (trader wins/losses affect this)
    mapping(address => uint256) public override poolAmounts;

    // Tracks tokens reserved for open positions
    mapping(address => uint256) public override reservedAmounts;

    event Deposit(address token, uint256 amount);
    event Withdraw(address token, uint256 amount, address to);
    event IncreasePoolAmount(address token, uint256 amount);
    event DecreasePoolAmount(address token, uint256 amount);

    constructor() Ownable(msg.sender) {}

    modifier onlyMarket() {
        require(msg.sender == market, "PerpVault: FORBIDDEN");
        _;
    }

    function setMarket(address _market) external onlyOwner {
        market = _market;
    }

    // ============ Liquidity Provider Functions ============

    // In a real system, this would mint LP tokens (like GLP).
    // Simplified here: just deposit/withdraw for admin/testing or direct LPing without token minting yet.
    function addLiquidity(address token, uint256 amount) external nonReentrant {
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        poolAmounts[token] += amount;
        emit Deposit(token, amount);
    }

    function removeLiquidity(
        address token,
        uint256 amount,
        address to
    ) external onlyOwner nonReentrant {
        require(poolAmounts[token] >= amount, "PerpVault: INSUFFICIENT_POOL");
        // Ensure we don't withdraw reserved collateral
        require(
            poolAmounts[token] - reservedAmounts[token] >= amount,
            "PerpVault: RESERVED"
        );

        poolAmounts[token] -= amount;
        IERC20(token).safeTransfer(to, amount);
        emit Withdraw(token, amount, to);
    }

    // ============ Market Functions ============

    function deposit(
        address token,
        uint256 amount
    ) external override onlyMarket {
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        poolAmounts[token] += amount;
        emit Deposit(token, amount);
    }

    function withdraw(
        address token,
        uint256 amount,
        address to
    ) external override onlyMarket {
        require(poolAmounts[token] >= amount, "PerpVault: INSUFFICIENT_POOL");
        require(
            poolAmounts[token] - reservedAmounts[token] >= amount,
            "PerpVault: RESERVED"
        );
        poolAmounts[token] -= amount;
        IERC20(token).safeTransfer(to, amount);
        emit Withdraw(token, amount, to);
    }

    function increasePoolAmount(
        address token,
        uint256 amount
    ) external override onlyMarket {
        poolAmounts[token] += amount;
        emit IncreasePoolAmount(token, amount);
    }

    function decreasePoolAmount(
        address token,
        uint256 amount
    ) external override onlyMarket {
        require(
            poolAmounts[token] >= amount,
            "PerpVault: POOL_AMOUNT_EXCEEDED"
        );
        poolAmounts[token] -= amount;
        emit DecreasePoolAmount(token, amount);
    }

    function increaseReservedAmount(
        address token,
        uint256 amount
    ) external override onlyMarket {
        reservedAmounts[token] += amount;
        require(
            reservedAmounts[token] <= poolAmounts[token],
            "PerpVault: INSUFFICIENT_POOL_FOR_RESERVE"
        );
    }

    function decreaseReservedAmount(
        address token,
        uint256 amount
    ) external override onlyMarket {
        require(
            reservedAmounts[token] >= amount,
            "PerpVault: ID_RESERVE_EXCEEDED"
        );
        reservedAmounts[token] -= amount;
    }
}
