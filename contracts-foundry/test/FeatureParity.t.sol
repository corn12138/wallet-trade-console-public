// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {TradingPairFactory} from "../src/core/TradingPairFactory.sol";
import {PerpOracle} from "../src/core/PerpOracle.sol";
import {PerpVault} from "../src/core/PerpVault.sol";
import {PerpMarket} from "../src/core/PerpMarket.sol";
import {Router} from "../src/periphery/Router.sol";
import {PositionManager} from "../src/periphery/PositionManager.sol";
import {TokenFactory} from "../src/launchpad/TokenFactory.sol";
import {LaunchToken} from "../src/launchpad/LaunchToken.sol";
import {OnchainArtworkNFT} from "../src/nft/OnchainArtworkNFT.sol";
import {IpfsArtworkNFT} from "../src/nft/IpfsArtworkNFT.sol";
import {MockERC20} from "../src/tokens/MockERC20.sol";

contract FeatureParityTest is Test {
    uint256 internal constant CREATION_FEE = 0.001 ether;
    uint256 internal constant PRICE_PRECISION = 10 ** 30;

    address internal creator = makeAddr("creator");
    address internal trader = makeAddr("trader");
    address internal feeReceiver = makeAddr("feeReceiver");
    address internal collector = makeAddr("collector");

    MockERC20 internal usdc;
    MockERC20 internal weth;
    TradingPairFactory internal factory;
    Router internal router;
    TokenFactory internal tokenFactory;
    PerpOracle internal oracle;
    PerpVault internal vault;
    PerpMarket internal market;
    PositionManager internal positionManager;
    OnchainArtworkNFT internal onchainArtworkNft;
    IpfsArtworkNFT internal ipfsArtworkNft;

    function setUp() public {
        usdc = new MockERC20("Mock USDC", "USDC", 18);
        weth = new MockERC20("Mock WETH", "WETH", 18);

        factory = new TradingPairFactory(address(this));
        router = new Router(address(factory), address(weth));
        tokenFactory = new TokenFactory(CREATION_FEE, feeReceiver, address(router));

        oracle = new PerpOracle();
        vault = new PerpVault();
        market = new PerpMarket(address(vault), address(oracle), feeReceiver);
        positionManager = new PositionManager(address(market), address(oracle));

        vault.setMarket(address(market));
        market.setPositionManager(address(positionManager));

        oracle.setManualPrice(address(usdc), 1 * PRICE_PRECISION);
        oracle.setManualPrice(address(weth), 3_450 * PRICE_PRECISION);

        usdc.mint(address(this), 500_000 ether);
        weth.mint(address(this), 500 ether);
        usdc.approve(address(vault), type(uint256).max);
        weth.approve(address(vault), type(uint256).max);
        vault.addLiquidity(address(usdc), 500_000 ether);
        vault.addLiquidity(address(weth), 500 ether);

        usdc.mint(trader, 100_000 ether);

        onchainArtworkNft = new OnchainArtworkNFT(
            "AI Mint Ticket",
            "AMT",
            "An on-chain SVG ERC721 collection for testnet demos.",
            "#0f172a"
        );
        ipfsArtworkNft = new IpfsArtworkNFT("AI Mint Ticket IPFS", "AMTI");
    }

    function test_CreateAndLaunchMatchesFrontendFlow() public {
        vm.deal(creator, 1 ether);

        vm.prank(creator);
        (address token, address bondingCurve) = tokenFactory.createAndLaunch{value: CREATION_FEE}(
            "Launch Asset",
            "LCH",
            "Token created from the create-token page flow.",
            "https://example.com/token.png",
            "https://example.com/banner.png",
            0
        );

        assertEq(tokenFactory.totalTokens(), 1);
        assertEq(tokenFactory.tokenToBondingCurve(token), bondingCurve);
        assertEq(LaunchToken(token).bondingCurve(), bondingCurve);
        assertEq(LaunchToken(token).owner(), creator);
        assertTrue(LaunchToken(token).launched());
    }

    function test_PerpSuiteSupportsOpenAndClosePosition() public {
        uint256 collateralAmount = 100 ether;
        uint256 sizeDelta = 500 * PRICE_PRECISION;
        uint256 executionPrice = 3_450 * PRICE_PRECISION;
        uint256 deadline = block.timestamp + 1 hours;

        vm.startPrank(trader);
        usdc.approve(address(positionManager), collateralAmount);
        positionManager.openPosition(
            address(weth),
            address(usdc),
            collateralAmount,
            sizeDelta,
            true,
            executionPrice,
            deadline
        );
        vm.stopPrank();

        bytes32 key = market.getPositionKey(trader, address(weth), address(usdc), true);
        (uint256 size,,,,,,) = market.positions(key);
        assertEq(size, sizeDelta);
        assertEq(oracle.getPrice(address(weth)), executionPrice);

        vm.prank(trader);
        positionManager.closePosition(
            address(weth),
            address(usdc),
            0,
            sizeDelta,
            true,
            executionPrice,
            deadline
        );

        (uint256 sizeAfter,,,,,,) = market.positions(key);
        assertEq(sizeAfter, 0);
    }

    function test_NftContractsSupportStudioMintFlows() public {
        uint256 onchainTokenId = onchainArtworkNft.mintArtwork(
            collector,
            "Parity Piece",
            "Minted from Foundry smoke coverage.",
            "#22c55e"
        );
        assertEq(onchainArtworkNft.ownerOf(onchainTokenId), collector);
        assertGt(bytes(onchainArtworkNft.tokenURI(onchainTokenId)).length, 0);

        uint256 ipfsTokenId = ipfsArtworkNft.mintWithTokenURI(
            collector,
            "ipfs://bafybeigdyrzt4-example-metadata"
        );
        assertEq(ipfsArtworkNft.ownerOf(ipfsTokenId), collector);
        assertEq(
            ipfsArtworkNft.tokenURI(ipfsTokenId),
            "ipfs://bafybeigdyrzt4-example-metadata"
        );
    }
}
