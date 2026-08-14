// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "./LaunchToken.sol";
import "./BondingCurve.sol";

/**
 * @title TokenFactory
 * @dev Factory contract for creating launch tokens with bonding curves
 * Supports DEX graduation with Uniswap V2/PancakeSwap
 */
contract TokenFactory is Ownable, ReentrancyGuard {
    // Configuration
    uint256 public creationFee;
    address public feeRecipient;
    address public dexRouter; // Uniswap V2 / PancakeSwap router

    // Default bonding curve parameters
    uint256 public defaultBasePrice = 0.00001 ether;
    uint256 public defaultSlope = 0.000000001 ether;
    uint256 public defaultReserveRatio = 5000; // 50%
    uint256 public defaultTradeFee = 300; // 3%
    uint256 public defaultGraduationThreshold = 100 ether; // 100 ETH market cap

    // Token tracking
    address[] public allTokens;
    mapping(address => address) public tokenToBondingCurve;
    mapping(address => address[]) public creatorTokens;

    // Events
    event TokenCreated(
        address indexed token,
        address indexed bondingCurve,
        address indexed creator,
        string symbol,
        string name
    );
    event ConfigUpdated(string configName, uint256 value);
    event AddressConfigUpdated(string configName, address value);

    constructor(
        uint256 creationFee_,
        address feeRecipient_,
        address dexRouter_
    ) Ownable(msg.sender) {
        creationFee = creationFee_;
        feeRecipient = feeRecipient_;
        dexRouter = dexRouter_;
    }

    /**
     * @dev Create a new launch token with bonding curve
     */
    function createToken(
        string memory name,
        string memory symbol,
        string memory description,
        string memory image,
        string memory banner
    )
        external
        payable
        nonReentrant
        returns (address token, address bondingCurve)
    {
        require(msg.value >= creationFee, "Insufficient creation fee");
        (token, bondingCurve) = _createToken(name, symbol, description, image, banner, msg.sender);

        // Transfer token ownership to creator
        LaunchToken(token).transferOwnership(msg.sender);

        // Refund excess ETH
        if (msg.value > creationFee) {
            (bool refundSuccess, ) = msg.sender.call{
                value: msg.value - creationFee
            }("");
            require(refundSuccess, "Refund failed");
        }
    }

    /**
     * @dev Create and immediately launch token with initial buy
     */
    function createAndLaunch(
        string memory name,
        string memory symbol,
        string memory description,
        string memory image,
        string memory banner,
        uint256 initialMint
    )
        external
        payable
        nonReentrant
        returns (address token, address bondingCurve)
    {
        require(msg.value >= creationFee, "Insufficient creation fee");

        // Create token and curve using internal function (avoids self-call reentrancy)
        (token, bondingCurve) = _createToken(name, symbol, description, image, banner, msg.sender);

        // Launch the token — factory is the initial owner, so this works
        LaunchToken(token).launch(initialMint);

        // Transfer ownership to the creator after launch
        LaunchToken(token).transferOwnership(msg.sender);

        // Initial buy with remaining ETH
        uint256 buyAmount = msg.value - creationFee;
        if (buyAmount > 0) {
            BondingCurve(payable(bondingCurve)).buy{value: buyAmount}(0);
        }
    }

    /**
     * @dev Internal token + bonding curve creation logic
     */
    function _createToken(
        string memory name,
        string memory symbol,
        string memory description,
        string memory image,
        string memory banner,
        address creator
    ) internal returns (address token, address bondingCurve) {
        require(dexRouter != address(0), "DEX router not set");

        // Create token — factory is the initial owner so createAndLaunch can call launch()
        LaunchToken newToken = new LaunchToken(
            name,
            symbol,
            description,
            image,
            banner,
            address(this)
        );
        token = address(newToken);

        // Create bonding curve with DEX integration
        BondingCurve newCurve = new BondingCurve(
            token,
            defaultBasePrice,
            defaultSlope,
            defaultReserveRatio,
            defaultTradeFee,
            feeRecipient,
            defaultGraduationThreshold,
            dexRouter,
            creator
        );
        bondingCurve = address(newCurve);

        // Link token to bonding curve
        newToken.setBondingCurve(bondingCurve);

        // Track token
        allTokens.push(token);
        tokenToBondingCurve[token] = bondingCurve;
        creatorTokens[creator].push(token);

        // Transfer creation fee
        if (creationFee > 0 && feeRecipient != address(0)) {
            (bool success, ) = feeRecipient.call{value: creationFee}("");
            require(success, "Fee transfer failed");
        }

        emit TokenCreated(token, bondingCurve, creator, symbol, name);
    }

    /**
     * @dev Get total number of tokens created
     */
    function totalTokens() external view returns (uint256) {
        return allTokens.length;
    }

    /**
     * @dev Get tokens by creator
     */
    function getTokensByCreator(
        address creator
    ) external view returns (address[] memory) {
        return creatorTokens[creator];
    }

    /**
     * @dev Get paginated list of tokens
     */
    function getTokens(
        uint256 offset,
        uint256 limit
    ) external view returns (address[] memory tokens) {
        uint256 total = allTokens.length;
        if (offset >= total) return new address[](0);

        uint256 end = offset + limit;
        if (end > total) end = total;

        tokens = new address[](end - offset);
        for (uint256 i = offset; i < end; i++) {
            tokens[i - offset] = allTokens[i];
        }
    }

    /**
     * @dev Get bonding curve info for UI display
     */
    function getBondingCurveInfo(
        address token_
    )
        external
        view
        returns (
            uint256 price,
            uint256 marketCap_,
            uint256 progress_,
            bool graduated_,
            uint256 reserveBalance_
        )
    {
        address curveAddr = tokenToBondingCurve[token_];
        require(curveAddr != address(0), "Token not found");

        BondingCurve curve = BondingCurve(payable(curveAddr));
        price = curve.currentPrice();
        marketCap_ = curve.marketCap();
        progress_ = curve.progress();
        graduated_ = curve.graduated();
        reserveBalance_ = curve.reserveBalance();
    }

    // ============ Admin Functions ============

    function setCreationFee(uint256 fee) external onlyOwner {
        creationFee = fee;
        emit ConfigUpdated("creationFee", fee);
    }

    function setFeeRecipient(address recipient) external onlyOwner {
        feeRecipient = recipient;
        emit AddressConfigUpdated("feeRecipient", recipient);
    }

    function setDexRouter(address router) external onlyOwner {
        dexRouter = router;
        emit AddressConfigUpdated("dexRouter", router);
    }

    function setDefaultBasePrice(uint256 price) external onlyOwner {
        defaultBasePrice = price;
        emit ConfigUpdated("defaultBasePrice", price);
    }

    function setDefaultSlope(uint256 newSlope) external onlyOwner {
        defaultSlope = newSlope;
        emit ConfigUpdated("defaultSlope", newSlope);
    }

    function setDefaultTradeFee(uint256 fee) external onlyOwner {
        require(fee <= 1000, "Fee too high");
        defaultTradeFee = fee;
        emit ConfigUpdated("defaultTradeFee", fee);
    }

    function setDefaultGraduationThreshold(
        uint256 threshold
    ) external onlyOwner {
        defaultGraduationThreshold = threshold;
        emit ConfigUpdated("defaultGraduationThreshold", threshold);
    }
}
