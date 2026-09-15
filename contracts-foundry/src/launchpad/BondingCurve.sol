// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/Pausable.sol";
import "./LaunchToken.sol";

// Uniswap V2 Router Interface (also compatible with PancakeSwap)
interface IUniswapV2Router02 {
    function WETH() external pure returns (address);

    function addLiquidityETH(
        address token,
        uint256 amountTokenDesired,
        uint256 amountTokenMin,
        uint256 amountEthMin,
        address to,
        uint256 deadline
    )
        external
        payable
        returns (uint256 amountToken, uint256 amountEth, uint256 liquidity);

    function factory() external pure returns (address);
}

interface IUniswapV2Factory {
    function getPair(address tokenA, address tokenB) external view returns (address pair);
}

/**
 * @title BondingCurve
 * @dev Implements a linear bonding curve for token pricing with DEX graduation
 * Price = basePrice + (slope * supply)
 *
 * Features:
 * - Buy/Sell with slippage protection
 * - Automatic graduation to DEX when market cap threshold reached
 * - Emergency pause functionality
 * - Trading fees
 */
contract BondingCurve is ReentrancyGuard, Ownable, Pausable {
    using SafeERC20 for IERC20;

    // Token reference
    LaunchToken public token;

    // Curve parameters
    uint256 public basePrice; // Base price in wei
    uint256 public slope; // Price increase per token
    uint256 public reserveRatio; // Reserve ratio (in basis points, e.g., 5000 = 50%)

    // Fee configuration
    uint256 public tradeFee; // Trading fee in basis points (e.g., 300 = 3%)
    address public feeRecipient;

    // Reserve tracking
    uint256 public reserveBalance;

    // Launch thresholds
    uint256 public graduationThreshold; // Market cap threshold to graduate to DEX
    bool public graduated;

    // Anti-MEV / Sandwich protection
    uint256 public maxTradesPerBlock; // Max trades per user per block (0 = no limit)
    mapping(address => uint256) private _lastTradeBlock;
    mapping(address => uint256) private _tradesInBlock;

    // DEX Integration
    IUniswapV2Router02 public dexRouter;
    address public lpToken; // LP token address after graduation
    address public lpRecipient; // Where to send LP tokens (burn/lock address)
    uint256 public lpLockDuration; // Duration to lock LP (in seconds)
    uint256 public lpUnlockTime; // When LP can be withdrawn

    // Graduation configuration
    uint256 public liquidityPercent; // Percentage of reserve for liquidity (e.g., 8000 = 80%)
    uint256 public creatorPercent; // Percentage for creator (e.g., 500 = 5%)
    address public creatorAddress;

    // Events
    event Buy(address indexed buyer, uint256 ethIn, uint256 tokensOut, uint256 newPrice);
    event Sell(address indexed seller, uint256 tokensIn, uint256 ethOut, uint256 newPrice);
    event FeesCollected(address indexed recipient, uint256 amount);
    event Graduated(uint256 marketCap, uint256 timestamp, address lpToken, uint256 lpAmount);
    event LPWithdrawn(address indexed recipient, uint256 amount);
    /// @notice Emitted when graduation queues an ETH payout for later claim.
    event PaymentQueued(address indexed recipient, uint256 amount, string tag);
    /// @notice Emitted when a queued ETH payout is claimed by its recipient.
    event PaymentClaimed(address indexed recipient, uint256 amount);

    /// @notice Pull-payment ledger for trade fees and graduation allocations.
    mapping(address => uint256) public pendingPayments;
    /// @notice Aggregate liability used to keep every queued payment fully backed by ETH.
    uint256 public totalPendingPayments;

    constructor(
        address token_,
        uint256 basePrice_,
        uint256 slope_,
        uint256 reserveRatio_,
        uint256 tradeFee_,
        address feeRecipient_,
        uint256 graduationThreshold_,
        address dexRouter_,
        address creatorAddress_
    )
        Ownable(msg.sender)
    {
        token = LaunchToken(token_);
        basePrice = basePrice_;
        slope = slope_;
        reserveRatio = reserveRatio_;
        tradeFee = tradeFee_;
        feeRecipient = feeRecipient_;
        graduationThreshold = graduationThreshold_;
        dexRouter = IUniswapV2Router02(dexRouter_);
        creatorAddress = creatorAddress_;

        // Default: 80% liquidity, 5% creator, 15% protocol
        liquidityPercent = 8000;
        creatorPercent = 500;

        // Default: LP tokens sent to burn address
        lpRecipient = address(0xdead);
        lpLockDuration = 365 days;

        // Anti-MEV: max 3 trades per user per block
        maxTradesPerBlock = 3;
    }

    /**
     * @dev Calculate current price based on supply
     */
    function currentPrice() public view returns (uint256) {
        uint256 supply = token.totalSupply();
        return basePrice + ((slope * supply) / 1e18);
    }

    /**
     * @dev Calculate price for buying a specific amount of tokens
     * Uses the average price over the supply range
     */
    function getBuyPrice(uint256 tokenAmount) public view returns (uint256 cost) {
        uint256 currentSupply = token.totalSupply();

        // Average price = basePrice + slope * (currentSupply + newSupply/2)
        uint256 avgPrice = basePrice + ((slope * (currentSupply + tokenAmount / 2)) / 1e18);
        cost = (avgPrice * tokenAmount) / 1e18;

        // Add trading fee
        cost = cost + ((cost * tradeFee) / 10_000);
    }

    /**
     * @dev Calculate return for selling a specific amount of tokens
     */
    function getSellPrice(uint256 tokenAmount) public view returns (uint256 payout) {
        uint256 currentSupply = token.totalSupply();
        require(tokenAmount <= currentSupply, "Insufficient supply");

        // Average price = basePrice + slope * (currentSupply - tokenAmount/2)
        uint256 avgPrice = basePrice + ((slope * (currentSupply - tokenAmount / 2)) / 1e18);
        payout = (avgPrice * tokenAmount) / 1e18;

        // Deduct trading fee
        payout = payout - ((payout * tradeFee) / 10_000);

        // Limit payout to available reserve
        if (payout > reserveBalance) {
            payout = reserveBalance;
        }
    }

    /**
     * @dev Buy tokens with ETH (enhanced slippage + MEV protection)
     * @param minTokens Minimum tokens to receive (slippage protection)
     * @param deadline Transaction deadline timestamp (0 = no deadline)
     * @param maxPrice Maximum acceptable price per token in wei (0 = no limit)
     */
    function buy(
        uint256 minTokens,
        uint256 deadline,
        uint256 maxPrice
    )
        external
        payable
        nonReentrant
        whenNotPaused
    {
        _buy(msg.sender, minTokens, deadline, maxPrice);
    }

    /**
     * @dev Backwards-compatible buy with only minTokens
     */
    function buy(uint256 minTokens) external payable nonReentrant whenNotPaused {
        _buy(msg.sender, minTokens, 0, 0);
    }

    /**
     * @notice Buy tokens for a recipient when another contract supplies the ETH.
     * @dev The recipient owns the minted tokens and is the buyer recorded by the event and trade
     * limit. Only the curve owner may delegate so arbitrary payers cannot consume another user's
     * per-block allowance.
     */
    function buyFor(
        address recipient,
        uint256 minTokens
    )
        external
        payable
        onlyOwner
        nonReentrant
        whenNotPaused
    {
        require(recipient != address(0), "Invalid recipient");
        _buy(recipient, minTokens, 0, 0);
    }

    function _buy(
        address recipient,
        uint256 minTokens,
        uint256 deadline,
        uint256 maxPrice
    )
        internal
    {
        require(!graduated, "Token has graduated to DEX");
        require(msg.value > 0, "Must send ETH");

        // Deadline protection: prevent stale transactions
        if (deadline > 0) {
            require(block.timestamp <= deadline, "Transaction expired");
        }

        // Front-running protection: reject if price moved too high
        if (maxPrice > 0) {
            require(currentPrice() <= maxPrice, "Price too high");
        }

        // Sandwich attack protection: limit trades per block
        _enforceTradeLimits(recipient);

        // Calculate tokens based on ETH sent
        uint256 ethAfterFee = msg.value - ((msg.value * tradeFee) / 10_000);
        uint256 fee = msg.value - ethAfterFee;

        // Calculate tokens to mint
        uint256 tokenAmount = estimateTokensForEth(ethAfterFee);
        require(tokenAmount >= minTokens, "Slippage exceeded");

        // Update reserve
        reserveBalance += ethAfterFee;

        // Mint tokens to buyer
        token.mint(recipient, tokenAmount);

        // Transfer fee
        if (fee > 0 && feeRecipient != address(0)) {
            _queuePayment(feeRecipient, fee);
            emit FeesCollected(feeRecipient, fee);
        }

        emit Buy(recipient, msg.value, tokenAmount, currentPrice());

        // Check graduation
        _checkGraduation();
    }

    /**
     * @dev Sell tokens for ETH (enhanced with deadline)
     * @param tokenAmount Amount of tokens to sell
     * @param minEth Minimum ETH to receive (slippage protection)
     * @param deadline Transaction deadline timestamp (0 = no deadline)
     */
    function sell(
        uint256 tokenAmount,
        uint256 minEth,
        uint256 deadline
    )
        external
        nonReentrant
        whenNotPaused
    {
        require(!graduated, "Token has graduated to DEX");
        require(tokenAmount > 0, "Must sell tokens");
        require(token.balanceOf(msg.sender) >= tokenAmount, "Insufficient balance");

        // Deadline protection
        if (deadline > 0) {
            require(block.timestamp <= deadline, "Transaction expired");
        }

        require(tokenAmount <= token.totalSupply(), "Exceeds supply");

        // Sandwich attack protection
        _enforceTradeLimits(msg.sender);

        // Calculate ETH payout
        uint256 grossPayout = _calculateSellGross(tokenAmount);
        uint256 fee = (grossPayout * tradeFee) / 10_000;
        uint256 netPayout = grossPayout - fee;

        // Cap total outflow to available reserve
        if (grossPayout > reserveBalance) {
            grossPayout = reserveBalance;
            fee = (grossPayout * tradeFee) / 10_000;
            netPayout = grossPayout - fee;
        }
        require(netPayout >= minEth, "Slippage exceeded");

        // Deduct grossPayout (netPayout + fee) as single operation
        reserveBalance -= grossPayout;

        // Burn tokens first (CEI pattern)
        token.burn(msg.sender, tokenAmount);

        // Transfer ETH to seller
        (bool success,) = msg.sender.call{ value: netPayout }("");
        require(success, "ETH transfer failed");

        // Transfer fee
        if (fee > 0 && feeRecipient != address(0)) {
            _queuePayment(feeRecipient, fee);
            emit FeesCollected(feeRecipient, fee);
        }

        emit Sell(msg.sender, tokenAmount, netPayout, currentPrice());
    }

    /**
     * @dev Backwards-compatible sell with only minEth
     */
    function sell(uint256 tokenAmount, uint256 minEth) external nonReentrant whenNotPaused {
        require(!graduated, "Token has graduated to DEX");
        require(tokenAmount > 0, "Must sell tokens");
        require(token.balanceOf(msg.sender) >= tokenAmount, "Insufficient balance");
        require(tokenAmount <= token.totalSupply(), "Exceeds supply");

        _enforceTradeLimits(msg.sender);

        uint256 grossPayout = _calculateSellGross(tokenAmount);
        uint256 fee = (grossPayout * tradeFee) / 10_000;
        uint256 netPayout = grossPayout - fee;

        if (grossPayout > reserveBalance) {
            grossPayout = reserveBalance;
            fee = (grossPayout * tradeFee) / 10_000;
            netPayout = grossPayout - fee;
        }
        require(netPayout >= minEth, "Slippage exceeded");

        reserveBalance -= grossPayout;

        token.burn(msg.sender, tokenAmount);

        (bool success,) = msg.sender.call{ value: netPayout }("");
        require(success, "ETH transfer failed");

        if (fee > 0 && feeRecipient != address(0)) {
            _queuePayment(feeRecipient, fee);
            emit FeesCollected(feeRecipient, fee);
        }

        emit Sell(msg.sender, tokenAmount, netPayout, currentPrice());
    }

    /**
     * @dev Estimate tokens for a given ETH amount
     * Uses integral of the price curve for accuracy:
     * ETH = basePrice * deltaTokens + slope * (S1^2 - S0^2) / (2 * 1e18)
     * Solving for deltaTokens via quadratic formula
     */
    function estimateTokensForEth(uint256 ethAmount) public view returns (uint256) {
        uint256 currentSupply = token.totalSupply();

        if (slope == 0) {
            // Constant price case
            if (basePrice == 0) return 0;
            return (ethAmount * 1e18) / basePrice;
        }

        // Quadratic: (slope/2) * t^2 + (basePrice + slope*S0) * t - ethAmount*1e18 = 0
        // a = slope / 2, b = basePrice + slope * S0 / 1e18, c = -ethAmount
        // t = (-b + sqrt(b^2 + 4ac)) / (2a)
        // Simplification with scaling for fixed-point math:
        uint256 b = basePrice + (slope * currentSupply) / 1e18;
        // discriminant = b^2 + 2 * slope * ethAmount
        uint256 discriminant = b * b + (2 * slope * ethAmount);

        // sqrt using Babylonian method
        uint256 sqrtDisc = _sqrt(discriminant);

        if (sqrtDisc <= b) return 0;

        // tokens = (sqrtDisc - b) * 1e18 / slope
        uint256 tokens = ((sqrtDisc - b) * 1e18) / slope;
        return tokens;
    }

    /**
     * @dev Babylonian square root
     */
    function _sqrt(uint256 x) internal pure returns (uint256) {
        if (x == 0) return 0;
        uint256 z = (x + 1) / 2;
        uint256 y = x;
        while (z < y) {
            y = z;
            z = (x / z + z) / 2;
        }
        return y;
    }

    /**
     * @dev Calculate gross payout for selling tokens (before fee)
     */
    function _calculateSellGross(uint256 tokenAmount) internal view returns (uint256) {
        uint256 currentSupply = token.totalSupply();
        uint256 avgPrice = basePrice + ((slope * (currentSupply - tokenAmount / 2)) / 1e18);
        return (avgPrice * tokenAmount) / 1e18;
    }

    /**
     * @dev Enforce per-block trade limits for anti-sandwich protection
     */
    function _enforceTradeLimits(address trader) internal {
        if (maxTradesPerBlock == 0) return; // Disabled

        if (_lastTradeBlock[trader] == block.number) {
            _tradesInBlock[trader]++;
            require(_tradesInBlock[trader] <= maxTradesPerBlock, "Too many trades this block");
        } else {
            _lastTradeBlock[trader] = block.number;
            _tradesInBlock[trader] = 1;
        }
    }

    /**
     * @dev Get current market cap
     */
    function marketCap() public view returns (uint256) {
        return (currentPrice() * token.totalSupply()) / 1e18;
    }

    /**
     * @dev Get bonding curve progress (0-10000 basis points)
     */
    function progress() public view returns (uint256) {
        uint256 mc = marketCap();
        if (mc >= graduationThreshold) return 10_000;
        return (mc * 10_000) / graduationThreshold;
    }

    /**
     * @dev Check if token should graduate to DEX and execute graduation
     */
    function _checkGraduation() internal {
        if (marketCap() >= graduationThreshold && !graduated) {
            _graduate();
        }
    }

    /**
     * @dev Execute graduation: add liquidity to DEX
     */
    function _graduate() internal {
        graduated = true;

        // Calculate amounts for liquidity
        uint256 curveReserve = reserveBalance;
        uint256 ethForLiquidity = (curveReserve * liquidityPercent) / 10_000;
        uint256 ethForCreator =
            creatorAddress == address(0) ? 0 : (curveReserve * creatorPercent) / 10_000;

        // Mint tokens for liquidity pool (equal value to ETH)
        uint256 tokenPrice = currentPrice();
        uint256 tokensForLiquidity = (ethForLiquidity * 1e18) / tokenPrice;

        // Mint tokens to this contract for adding liquidity
        token.mint(address(this), tokensForLiquidity);

        // Approve router to spend tokens
        token.approve(address(dexRouter), tokensForLiquidity);

        // Balance deltas are authoritative because a router can return unused ETH or tokens while
        // reporting requested amounts in its return tuple.
        uint256 balanceBeforeLiquidity = address(this).balance;
        uint256 tokenBalanceBeforeLiquidity = token.balanceOf(address(this));
        (uint256 amountToken, uint256 amountEth, uint256 liquidity) = dexRouter.addLiquidityETH{
            value: ethForLiquidity
        }(
            address(token),
            tokensForLiquidity,
            (tokensForLiquidity * 95) / 100, // 5% slippage for tokens
            (ethForLiquidity * 95) / 100, // 5% slippage for ETH
            address(this), // LP tokens to this contract (for locking)
            block.timestamp + 300 // 5 min deadline
        );
        uint256 balanceAfterLiquidity = address(this).balance;
        require(balanceAfterLiquidity <= balanceBeforeLiquidity, "Invalid router refund");
        uint256 ethSpent = balanceBeforeLiquidity - balanceAfterLiquidity;
        require(ethSpent == amountEth && ethSpent <= ethForLiquidity, "Invalid router accounting");

        uint256 tokenBalanceAfterLiquidity = token.balanceOf(address(this));
        require(tokenBalanceAfterLiquidity <= tokenBalanceBeforeLiquidity, "Invalid token refund");
        uint256 tokensSpent = tokenBalanceBeforeLiquidity - tokenBalanceAfterLiquidity;
        require(
            tokensSpent == amountToken && tokensSpent <= tokensForLiquidity,
            "Invalid token accounting"
        );

        // Graduation is terminal, so no router allowance or newly minted inventory should survive
        // the one liquidity call.
        token.approve(address(dexRouter), 0);
        uint256 unusedLiquidityTokens = tokensForLiquidity - tokensSpent;
        if (unusedLiquidityTokens > 0) {
            token.burn(address(this), unusedLiquidityTokens);
        }

        // Get LP token address
        address factory = dexRouter.factory();
        lpToken = IUniswapV2Factory(factory).getPair(address(token), dexRouter.WETH());

        // Set LP unlock time
        lpUnlockTime = block.timestamp + lpLockDuration;

        // Graduation allocates only the curve reserve; pre-existing fee credits remain separately
        // backed.
        reserveBalance = 0;

        if (ethForCreator > 0) {
            _queuePayment(creatorAddress, ethForCreator);
            emit PaymentQueued(creatorAddress, ethForCreator, "creator");
        }

        uint256 protocolAllocation = curveReserve - ethSpent - ethForCreator;
        if (protocolAllocation > 0 && feeRecipient != address(0)) {
            _queuePayment(feeRecipient, protocolAllocation);
            emit PaymentQueued(feeRecipient, protocolAllocation, "graduation_residual");
        }

        emit Graduated(marketCap(), block.timestamp, lpToken, liquidity);
    }

    /**
     * @dev Every ledger credit must remain backed before control returns to an external caller.
     */
    function _queuePayment(address recipient, uint256 amount) internal {
        pendingPayments[recipient] += amount;
        totalPendingPayments += amount;
        require(totalPendingPayments <= address(this).balance, "Payment ledger underfunded");
    }

    /**
     * @notice Claim ETH credited to `msg.sender` from prior graduations or
     *         fee allocations. Following the Checks-Effects-Interactions
     *         pattern: zero the balance before the external call so a
     *         re-entrant `receive()` cannot double-spend.
     */
    function claimPayment() external nonReentrant {
        uint256 amount = pendingPayments[msg.sender];
        require(amount > 0, "Nothing to claim");

        pendingPayments[msg.sender] = 0;
        totalPendingPayments -= amount;

        (bool success,) = msg.sender.call{ value: amount }("");
        require(success, "ETH transfer failed");

        emit PaymentClaimed(msg.sender, amount);
    }

    /**
     * @dev Manually trigger graduation (owner only) - useful if automatic check fails
     */
    function forceGraduate() external onlyOwner {
        require(!graduated, "Already graduated");
        require(marketCap() >= graduationThreshold, "Threshold not reached");
        _graduate();
    }

    /**
     * @dev Withdraw LP tokens after lock period (owner only)
     */
    function withdrawLP(address recipient) external onlyOwner {
        require(graduated, "Not graduated yet");
        require(block.timestamp >= lpUnlockTime, "LP still locked");
        require(lpToken != address(0), "No LP token");

        uint256 lpBalance = IERC20(lpToken).balanceOf(address(this));
        require(lpBalance > 0, "No LP to withdraw");

        IERC20(lpToken).safeTransfer(recipient, lpBalance);
        emit LPWithdrawn(recipient, lpBalance);
    }

    // ============ Admin Functions ============

    /**
     * @dev Update fee recipient (owner only)
     */
    function setFeeRecipient(address newRecipient) external onlyOwner {
        feeRecipient = newRecipient;
    }

    /**
     * @dev Update trade fee (owner only)
     */
    function setTradeFee(uint256 newFee) external onlyOwner {
        require(newFee <= 1000, "Fee too high"); // Max 10%
        tradeFee = newFee;
    }

    /**
     * @dev Update DEX router (owner only) - use with caution!
     */
    function setDexRouter(address newRouter) external onlyOwner {
        require(!graduated, "Cannot change after graduation");
        dexRouter = IUniswapV2Router02(newRouter);
    }

    /**
     * @dev Update graduation config (owner only)
     */
    function setGraduationConfig(
        uint256 _liquidityPercent,
        uint256 _creatorPercent,
        address _lpRecipient,
        uint256 _lpLockDuration
    )
        external
        onlyOwner
    {
        require(!graduated, "Cannot change after graduation");
        require(_liquidityPercent + _creatorPercent <= 10_000, "Invalid percentages");

        liquidityPercent = _liquidityPercent;
        creatorPercent = _creatorPercent;
        lpRecipient = _lpRecipient;
        lpLockDuration = _lpLockDuration;
    }

    /**
     * @dev Update max trades per block (owner only)
     */
    function setMaxTradesPerBlock(uint256 _maxTrades) external onlyOwner {
        maxTradesPerBlock = _maxTrades;
    }

    /**
     * @dev Emergency pause (owner only)
     */
    function pause() external onlyOwner {
        _pause();
    }

    /**
     * @dev Unpause (owner only)
     */
    function unpause() external onlyOwner {
        _unpause();
    }

    /**
     * @dev Emergency withdraw (owner only) - only when paused
     */
    function emergencyWithdraw(address recipient) external onlyOwner whenPaused {
        uint256 balance = address(this).balance;
        // Reserve and pull-payment balances are obligations, not emergency surplus.
        uint256 protectedBalance = reserveBalance + totalPendingPayments;
        require(balance > protectedBalance, "No excess ETH to withdraw");
        uint256 amount = balance - protectedBalance;

        (bool success,) = recipient.call{ value: amount }("");
        require(success, "Withdraw failed");
    }

    // Accept ETH only from dexRouter during graduation (addLiquidityETH refunds)
    receive() external payable {
        // Do NOT add to reserveBalance — only buy() should increase reserves
    }
}
