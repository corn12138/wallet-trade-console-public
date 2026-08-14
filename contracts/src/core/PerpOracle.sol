// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/access/Ownable.sol";

interface IChainlinkAggregator {
    function latestRoundData()
        external
        view
        returns (
            uint80 roundId,
            int256 answer,
            uint256 startedAt,
            uint256 updatedAt,
            uint80 answeredInRound
        );
}

contract PerpOracle is Ownable {
    uint256 public constant PRICE_PRECISION = 10 ** 30;

    mapping(address => address) public priceFeeds;
    mapping(address => uint256) public manualPrices; // Must be in 30 decimals
    mapping(address => uint256) public stalenessThresholds; // Per-feed staleness in seconds
    bool public isManualMode = true;

    event ManualPriceSet(address indexed token, uint256 price);
    event ManualModeChanged(bool isManual);

    constructor() Ownable(msg.sender) {}

    function setPriceFeed(address token, address feed, uint256 stalenessThreshold) external onlyOwner {
        priceFeeds[token] = feed;
        stalenessThresholds[token] = stalenessThreshold > 0 ? stalenessThreshold : 3600;
    }

    function setManualPrice(address token, uint256 price) external onlyOwner {
        manualPrices[token] = price;
        emit ManualPriceSet(token, price);
    }

    function setManualMode(bool _isManualMode) external onlyOwner {
        isManualMode = _isManualMode;
        emit ManualModeChanged(_isManualMode);
    }

    function getPrice(address token) external view returns (uint256) {
        if (isManualMode) {
            uint256 price = manualPrices[token];
            require(price > 0, "PerpOracle: INVALID_PRICE");
            return price;
        }

        address feed = priceFeeds[token];
        require(feed != address(0), "PerpOracle: NO_FEED");

        (
            uint80 roundId,
            int256 price,
            ,
            uint256 updatedAt,
            uint80 answeredInRound
        ) = IChainlinkAggregator(feed).latestRoundData();

        require(price > 0, "PerpOracle: INVALID_PRICE");
        uint256 staleness = stalenessThresholds[token] > 0 ? stalenessThresholds[token] : 3600;
        require(block.timestamp - updatedAt < staleness, "PerpOracle: STALE_PRICE");
        require(answeredInRound >= roundId, "PerpOracle: STALE_ROUND");

        // Chainlink returns 8 decimals, normalize to 30 decimals
        return uint256(price) * 10 ** 22;
    }
}
