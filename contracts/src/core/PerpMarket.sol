// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/utils/math/Math.sol";

import "../interfaces/IPerpVault.sol";
import "../interfaces/IPerpMarket.sol";
import "./PerpOracle.sol";

contract PerpMarket is IPerpMarket, Ownable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    address public vault;
    address public oracle;
    address public positionManager; // Periphery contract for router logic

    uint256 public constant FUNDING_RATE_PRECISION = 1000000;
    uint256 public constant LIQUIDATION_FEE_USD = 5 * 10 ** 30; // $5 (assuming 30 decimals for USD)
    uint256 public constant MIN_LEVERAGE = 10000; // 1x = 10000
    uint256 public constant MAX_LEVERAGE = 50 * 10000; // 50x
    uint256 public constant BASIS_POINTS_DIVISOR = 10000;
    uint256 public constant MARGIN_FEE_BASIS_POINTS = 10; // 0.1%
    uint256 public constant MAINTENANCE_MARGIN_BPS = 100; // 1% maintenance margin
    uint256 public constant PRICE_PRECISION = 10 ** 30;

    address public feeReceiver;

    // Open interest tracking for funding rate
    uint256 public globalLongSize;
    uint256 public globalShortSize;

    // Position key: keccak256(abi.encodePacked(account, indexToken, collateralToken, isLong))
    struct Position {
        uint256 size; // Position size in USD (30 decimals)
        uint256 collateral; // Collateral in USD (30 decimals)
        uint256 averagePrice; // Average entry price (30 decimals)
        uint256 entryFundingRate; // Funding rate at entry
        uint256 reserveAmount; // Amount reserved in Vault
        int256 realisedPnl; // Realised PnL
        uint256 lastUpdatedAt; // Timestamp
    }

    mapping(bytes32 => Position) public positions;
    mapping(address => uint256) public cumulativeFundingRates;
    mapping(address => uint256) public lastFundingTimes;

    // Config
    uint256 public fundingInterval = 1 hours;
    uint256 public fundingRateFactor = 100; // 0.01% per hour / skew
    uint256 public stableTaxBasisPoints = 5; // 0.05%

    event IncreasePosition(
        bytes32 key,
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 price,
        uint256 fee
    );

    event DecreasePosition(
        bytes32 key,
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 price,
        int256 pnl,
        uint256 fee
    );

    event LiquidatePosition(
        bytes32 key,
        address account,
        address indexToken,
        bool isLong,
        uint256 size,
        uint256 collateral,
        int256 pnl
    );

    constructor(address _vault, address _oracle, address _feeReceiver) Ownable(msg.sender) {
        vault = _vault;
        oracle = _oracle;
        feeReceiver = _feeReceiver;
    }

    function setFeeReceiver(address _feeReceiver) external onlyOwner {
        feeReceiver = _feeReceiver;
    }

    modifier onlyPositionManager() {
        require(msg.sender == positionManager, "PerpMarket: FORBIDDEN");
        _;
    }

    function setPositionManager(address _positionManager) external onlyOwner {
        positionManager = _positionManager;
    }

    // ============ Core Functions ============

    function openPosition(
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta, // Amount in TOKEN DECIMALS (if deposit)
        uint256 sizeDelta, // Size in USD (30 decimals)
        bool isLong,
        uint256 price
    ) external override onlyPositionManager nonReentrant {
        _updateFunding(indexToken);

        // 1. Approve Vault and deposit collateral
        if (collateralDelta > 0) {
            IERC20(collateralToken).forceApprove(vault, collateralDelta);
            IPerpVault(vault).deposit(collateralToken, collateralDelta);
        }

        bytes32 key = getPositionKey(
            account,
            indexToken,
            collateralToken,
            isLong
        );
        Position storage position = positions[key];

        // 2. Calculate and deduct fees
        uint256 fee = _calculateFee(sizeDelta);

        // 3. Update position with weighted average price
        uint256 markPrice = price;

        if (position.size == 0) {
            position.averagePrice = markPrice;
        } else {
            // Weighted average price
            position.averagePrice = (position.averagePrice * position.size + markPrice * sizeDelta) /
                (position.size + sizeDelta);
        }

        // Convert collateral to USD and deduct fee
        uint256 collateralValue = _tokenToUsd(collateralToken, collateralDelta);
        require(collateralValue > fee, "PerpMarket: FEE_EXCEEDS_COLLATERAL");
        position.collateral += (collateralValue - fee);

        position.size += sizeDelta;

        // 4. Validate leverage
        require(position.size > 0, "PerpMarket: ZERO_SIZE");
        uint256 leverage = (position.size * MIN_LEVERAGE) / position.collateral;
        require(leverage >= MIN_LEVERAGE, "PerpMarket: LEVERAGE_TOO_LOW");
        require(leverage <= MAX_LEVERAGE, "PerpMarket: MAX_LEVERAGE_EXCEEDED");

        position.entryFundingRate = cumulativeFundingRates[indexToken];
        position.lastUpdatedAt = block.timestamp;

        // 5. Reserve tokens in Vault (for both longs and shorts)
        if (isLong) {
            uint256 reserveDelta = _usdToToken(indexToken, sizeDelta);
            IPerpVault(vault).increaseReservedAmount(indexToken, reserveDelta);
            position.reserveAmount += reserveDelta;
            globalLongSize += sizeDelta;
        } else {
            uint256 reserveDelta = _usdToToken(collateralToken, sizeDelta);
            IPerpVault(vault).increaseReservedAmount(collateralToken, reserveDelta);
            position.reserveAmount += reserveDelta;
            globalShortSize += sizeDelta;
        }

        // 6. Transfer fee to fee receiver via vault
        if (fee > 0 && feeReceiver != address(0)) {
            uint256 feeTokens = _usdToToken(collateralToken, fee);
            if (feeTokens > 0) {
                IPerpVault(vault).withdraw(collateralToken, feeTokens, feeReceiver);
            }
        }

        emit IncreasePosition(
            key,
            account,
            indexToken,
            collateralToken,
            collateralDelta,
            sizeDelta,
            isLong,
            markPrice,
            fee
        );
    }

    function closePosition(
        address account,
        address indexToken,
        address collateralToken,
        uint256 /* collateralDelta */,
        uint256 sizeDelta,
        bool isLong,
        uint256 price
    ) external override onlyPositionManager nonReentrant {
        _updateFunding(indexToken);

        bytes32 key = getPositionKey(
            account,
            indexToken,
            collateralToken,
            isLong
        );
        Position storage position = positions[key];
        require(position.size > 0, "PerpMarket: NO_POSITION");

        // Cap sizeDelta to position size
        if (sizeDelta >= position.size) {
            sizeDelta = position.size;
        }

        // Calculate PnL
        (bool hasProfit, uint256 delta) = _calculatePnl(
            position,
            price,
            isLong
        );
        // Scale PnL proportionally to the size being closed
        uint256 pnlDelta = (delta * sizeDelta) / position.size;
        int256 pnl = hasProfit ? int256(pnlDelta) : -int256(pnlDelta);

        // Calculate fee
        uint256 fee = _calculateFee(sizeDelta);

        // Reduce reserve
        uint256 reserveDelta = (position.reserveAmount * sizeDelta) / position.size;
        if (isLong) {
            IPerpVault(vault).decreaseReservedAmount(indexToken, reserveDelta);
            globalLongSize -= sizeDelta;
        } else {
            IPerpVault(vault).decreaseReservedAmount(collateralToken, reserveDelta);
            globalShortSize -= sizeDelta;
        }
        position.reserveAmount -= reserveDelta;

        // Calculate collateral to return (proportional to size closed)
        uint256 collateralToReturn = (position.collateral * sizeDelta) / position.size;

        // Payout: collateral ± PnL - fee
        uint256 payoutUsd;
        if (hasProfit) {
            payoutUsd = collateralToReturn + pnlDelta;
            position.realisedPnl += int256(pnlDelta);
        } else {
            payoutUsd = pnlDelta >= collateralToReturn ? 0 : collateralToReturn - pnlDelta;
            position.realisedPnl -= int256(pnlDelta);
        }

        // Deduct fee from payout
        if (fee >= payoutUsd) {
            fee = payoutUsd;
            payoutUsd = 0;
        } else {
            payoutUsd -= fee;
        }

        // Update position state
        position.size -= sizeDelta;
        position.collateral -= collateralToReturn;

        // Withdraw payout to user
        if (payoutUsd > 0) {
            uint256 payoutTokens = _usdToToken(collateralToken, payoutUsd);
            IPerpVault(vault).withdraw(collateralToken, payoutTokens, account);
        }

        // Transfer fee
        if (fee > 0 && feeReceiver != address(0)) {
            uint256 feeTokens = _usdToToken(collateralToken, fee);
            if (feeTokens > 0) {
                IPerpVault(vault).withdraw(collateralToken, feeTokens, feeReceiver);
            }
        }

        // Clean up fully closed position
        if (position.size == 0) {
            delete positions[key];
        }

        emit DecreasePosition(
            key,
            account,
            indexToken,
            collateralToken,
            collateralToReturn,
            sizeDelta,
            isLong,
            price,
            pnl,
            fee
        );
    }

    function liquidatePosition(
        address account,
        address indexToken,
        address collateralToken,
        bool isLong,
        address liquidationFeeReceiver
    ) external override nonReentrant {
        _updateFunding(indexToken);

        bytes32 key = getPositionKey(account, indexToken, collateralToken, isLong);
        Position storage position = positions[key];
        require(position.size > 0, "PerpMarket: NO_POSITION");

        // Get current price from oracle
        uint256 currentPrice = PerpOracle(oracle).getPrice(indexToken);

        // Calculate PnL
        (bool hasProfit, uint256 delta) = _calculatePnl(position, currentPrice, isLong);

        // Calculate remaining collateral after PnL
        uint256 remainingCollateral;
        if (hasProfit) {
            remainingCollateral = position.collateral + delta;
        } else {
            remainingCollateral = delta >= position.collateral ? 0 : position.collateral - delta;
        }

        // Check if position is below maintenance margin
        uint256 maintenanceMargin = (position.size * MAINTENANCE_MARGIN_BPS) / BASIS_POINTS_DIVISOR;
        require(remainingCollateral < maintenanceMargin, "PerpMarket: NOT_LIQUIDATABLE");

        int256 pnl = hasProfit ? int256(delta) : -int256(delta);

        // Release reserves
        if (isLong) {
            IPerpVault(vault).decreaseReservedAmount(indexToken, position.reserveAmount);
            globalLongSize -= position.size;
        } else {
            IPerpVault(vault).decreaseReservedAmount(collateralToken, position.reserveAmount);
            globalShortSize -= position.size;
        }

        // Pay liquidation fee to liquidator
        if (remainingCollateral > 0 && liquidationFeeReceiver != address(0)) {
            uint256 liquidationFee = remainingCollateral > LIQUIDATION_FEE_USD
                ? LIQUIDATION_FEE_USD
                : remainingCollateral;
            uint256 feeTokens = _usdToToken(collateralToken, liquidationFee);
            if (feeTokens > 0) {
                IPerpVault(vault).withdraw(collateralToken, feeTokens, liquidationFeeReceiver);
            }
        }

        emit LiquidatePosition(
            key,
            account,
            indexToken,
            isLong,
            position.size,
            position.collateral,
            pnl
        );

        // Delete the position
        delete positions[key];
    }

    // ============ Helper functions ============

    function getPositionKey(
        address account,
        address indexToken,
        address collateralToken,
        bool isLong
    ) public pure returns (bytes32) {
        return keccak256(abi.encode(account, indexToken, collateralToken, isLong));
    }

    function _updateFunding(address indexToken) internal {
        if (lastFundingTimes[indexToken] == 0) {
            lastFundingTimes[indexToken] = block.timestamp;
            return;
        }
        if (block.timestamp - lastFundingTimes[indexToken] < fundingInterval) return;

        uint256 intervals = (block.timestamp - lastFundingTimes[indexToken]) / fundingInterval;

        // Funding rate based on long/short skew
        if (globalLongSize > 0 || globalShortSize > 0) {
            uint256 totalOI = globalLongSize + globalShortSize;
            uint256 skew = globalLongSize > globalShortSize
                ? globalLongSize - globalShortSize
                : globalShortSize - globalLongSize;
            uint256 fundingRate = (fundingRateFactor * skew * intervals) / totalOI;
            cumulativeFundingRates[indexToken] += fundingRate;
        }

        lastFundingTimes[indexToken] = block.timestamp;
    }

    function _calculatePnl(
        Position memory position,
        uint256 currentPrice,
        bool isLong
    ) internal pure returns (bool hasProfit, uint256 delta) {
        if (position.averagePrice == 0) return (false, 0);
        if (currentPrice > position.averagePrice) {
            hasProfit = isLong;
            delta =
                ((currentPrice - position.averagePrice) * position.size) /
                position.averagePrice;
        } else {
            hasProfit = !isLong;
            delta =
                ((position.averagePrice - currentPrice) * position.size) /
                position.averagePrice;
        }
    }

    function _calculateFee(uint256 sizeDelta) internal pure returns (uint256) {
        return (sizeDelta * MARGIN_FEE_BASIS_POINTS) / BASIS_POINTS_DIVISOR;
    }

    function _getTokenDecimals(address token) internal view returns (uint256) {
        return 10 ** IERC20Metadata(token).decimals();
    }

    function _tokenToUsd(
        address token,
        uint256 amount
    ) internal view returns (uint256) {
        if (amount == 0) return 0;
        uint256 tokenPrice = PerpOracle(oracle).getPrice(token); // 30 decimals
        uint256 decimals = _getTokenDecimals(token);
        // amount (token decimals) * price (30) / 10^decimals = 30
        return (amount * tokenPrice) / decimals;
    }

    function _usdToToken(
        address token,
        uint256 usdAmount
    ) internal view returns (uint256) {
        if (usdAmount == 0) return 0;
        uint256 tokenPrice = PerpOracle(oracle).getPrice(token);
        if (tokenPrice == 0) return 0;
        uint256 decimals = _getTokenDecimals(token);
        // usdAmount (30) * 10^decimals / price (30) = token decimals
        return (usdAmount * decimals) / tokenPrice;
    }
}
