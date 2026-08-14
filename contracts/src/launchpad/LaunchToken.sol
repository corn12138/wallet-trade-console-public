// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/**
 * @title LaunchToken
 * @dev ERC20 token with bonding curve support for launchpad functionality
 */
contract LaunchToken is ERC20, Ownable, ReentrancyGuard {
    // Token metadata
    string private _tokenImage;
    string private _tokenBanner;
    string public description;

    // Bonding curve reference
    address public bondingCurve;

    // Launch status
    bool public launched;
    uint256 public launchedAt;

    // Supply configuration
    uint256 public constant MAX_SUPPLY = 1_000_000_000 ether; // 1 billion tokens

    // Events
    event TokenLaunched(address indexed bondingCurve, uint256 timestamp);
    event MetadataUpdated(string image, string banner, string description);

    constructor(
        string memory name_,
        string memory symbol_,
        string memory description_,
        string memory image_,
        string memory banner_,
        address owner_
    ) ERC20(name_, symbol_) Ownable(owner_) {
        description = description_;
        _tokenImage = image_;
        _tokenBanner = banner_;
    }

    /**
     * @dev Set the bonding curve contract address
     * @param curve Address of the bonding curve contract
     */
    function setBondingCurve(address curve) external onlyOwner {
        require(curve != address(0), "Invalid bonding curve address");
        require(!launched, "Already launched");
        bondingCurve = curve;
    }

    /**
     * @dev Launch the token with bonding curve
     * @param initialMint Amount to mint initially
     */
    function launch(uint256 initialMint) external onlyOwner {
        require(!launched, "Already launched");
        require(bondingCurve != address(0), "Bonding curve not set");
        require(initialMint <= MAX_SUPPLY, "Exceeds max supply");

        launched = true;
        launchedAt = block.timestamp;

        if (initialMint > 0) {
            _mint(bondingCurve, initialMint);
        }

        emit TokenLaunched(bondingCurve, block.timestamp);
    }

    /**
     * @dev Mint tokens (only callable by bonding curve after launch)
     */
    function mint(address to, uint256 amount) external {
        require(launched, "Not launched");
        require(msg.sender == bondingCurve, "Only bonding curve can mint");
        require(totalSupply() + amount <= MAX_SUPPLY, "Exceeds max supply");

        _mint(to, amount);
    }

    /**
     * @dev Burn tokens (callable by bonding curve)
     */
    function burn(address from, uint256 amount) external {
        require(launched, "Not launched");
        require(msg.sender == bondingCurve, "Only bonding curve can burn");

        _burn(from, amount);
    }

    /**
     * @dev Update token metadata
     */
    function updateMetadata(
        string memory image_,
        string memory banner_,
        string memory description_
    ) external onlyOwner {
        _tokenImage = image_;
        _tokenBanner = banner_;
        description = description_;

        emit MetadataUpdated(image_, banner_, description_);
    }

    /**
     * @dev Get token image URL
     */
    function tokenImage() external view returns (string memory) {
        return _tokenImage;
    }

    /**
     * @dev Get token banner URL
     */
    function tokenBanner() external view returns (string memory) {
        return _tokenBanner;
    }
}
