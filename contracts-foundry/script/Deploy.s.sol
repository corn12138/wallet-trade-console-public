// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";
import {TradingPairFactory} from "../src/core/TradingPairFactory.sol";
import {PerpOracle} from "../src/core/PerpOracle.sol";
import {PerpVault} from "../src/core/PerpVault.sol";
import {PerpMarket} from "../src/core/PerpMarket.sol";
import {Router} from "../src/periphery/Router.sol";
import {PositionManager} from "../src/periphery/PositionManager.sol";
import {TokenFactory} from "../src/launchpad/TokenFactory.sol";
import {StakingPool} from "../src/launchpad/StakingPool.sol";
import {OnchainArtworkNFT} from "../src/nft/OnchainArtworkNFT.sol";
import {IpfsArtworkNFT} from "../src/nft/IpfsArtworkNFT.sol";
import {MockERC20} from "../src/tokens/MockERC20.sol";

abstract contract DeployUnifiedScript is Script {
    uint256 internal constant CREATION_FEE = 0.001 ether;
    uint256 internal constant REWARDS_DURATION = 30 days;
    uint256 internal constant PRICE_PRECISION = 10 ** 30;
    uint256 internal constant DEFAULT_DEADLINE_OFFSET = 1 hours;

    struct Deployment {
        address factory;
        address router;
        address mockUsdc;
        address mockWeth;
        address mockWbtc;
        address pair;
        address tokenFactory;
        address stakingToken;
        address stakingPool;
        address perpOracle;
        address perpVault;
        address perpMarket;
        address positionManager;
        address onchainArtworkNft;
        address ipfsArtworkNft;
    }

    function _deployUnified(address deployer) internal returns (Deployment memory deployment) {
        console.log("Deploying unified Foundry stack");
        console.log("Deployer:", deployer);

        MockERC20 mockUsdc = new MockERC20("Mock USDC", "USDC", 18);
        MockERC20 mockWeth = new MockERC20("Mock WETH", "WETH", 18);
        MockERC20 mockWbtc = new MockERC20("Mock WBTC", "WBTC", 8);

        TradingPairFactory factory = new TradingPairFactory(deployer);
        Router router = new Router(address(factory), address(mockWeth));
        address primaryPair = _seedAmmLiquidity(
            deployer,
            mockUsdc,
            mockWeth,
            mockWbtc,
            factory,
            router
        );

        TokenFactory tokenFactory = new TokenFactory(
            CREATION_FEE,
            deployer,
            address(router)
        );

        MockERC20 stakingToken = new MockERC20("Staking Token", "STK", 18);
        stakingToken.mint(deployer, 1_000_000 ether);
        StakingPool stakingPool = new StakingPool(
            address(stakingToken),
            address(stakingToken),
            REWARDS_DURATION
        );

        (PerpOracle oracle, PerpVault vault, PerpMarket market, PositionManager positionManager) =
            _deployPerpSuite(deployer, mockUsdc, mockWeth, mockWbtc);

        OnchainArtworkNFT onchainArtworkNft = new OnchainArtworkNFT(
            "AI Mint Ticket",
            "AMT",
            "An on-chain SVG ERC721 collection for testnet demos.",
            "#0f172a"
        );
        IpfsArtworkNFT ipfsArtworkNft = new IpfsArtworkNFT("AI Mint Ticket IPFS", "AMTI");

        deployment = Deployment({
            factory: address(factory),
            router: address(router),
            mockUsdc: address(mockUsdc),
            mockWeth: address(mockWeth),
            mockWbtc: address(mockWbtc),
            pair: primaryPair,
            tokenFactory: address(tokenFactory),
            stakingToken: address(stakingToken),
            stakingPool: address(stakingPool),
            perpOracle: address(oracle),
            perpVault: address(vault),
            perpMarket: address(market),
            positionManager: address(positionManager),
            onchainArtworkNft: address(onchainArtworkNft),
            ipfsArtworkNft: address(ipfsArtworkNft)
        });

        _logDeployment(deployment);
    }

    function _seedAmmLiquidity(
        address deployer,
        MockERC20 mockUsdc,
        MockERC20 mockWeth,
        MockERC20 mockWbtc,
        TradingPairFactory factory,
        Router router
    ) internal returns (address primaryPair) {
        mockUsdc.mint(deployer, 3_000_000 ether);
        mockWeth.mint(deployer, 3_000 ether);
        mockWbtc.mint(deployer, 150 * 10 ** 8);

        mockUsdc.approve(address(router), type(uint256).max);
        mockWeth.approve(address(router), type(uint256).max);
        mockWbtc.approve(address(router), type(uint256).max);

        router.addLiquidity(
            address(mockUsdc),
            address(mockWeth),
            600_000 ether,
            600 ether,
            0,
            0,
            deployer,
            block.timestamp + DEFAULT_DEADLINE_OFFSET
        );
        primaryPair = factory.getPair(address(mockUsdc), address(mockWeth));

        router.addLiquidity(
            address(mockUsdc),
            address(mockWbtc),
            300_000 ether,
            15 * 10 ** 8,
            0,
            0,
            deployer,
            block.timestamp + DEFAULT_DEADLINE_OFFSET
        );

        router.addLiquidity(
            address(mockWeth),
            address(mockWbtc),
            200 ether,
            6 * 10 ** 8,
            0,
            0,
            deployer,
            block.timestamp + DEFAULT_DEADLINE_OFFSET
        );
    }

    function _deployPerpSuite(
        address deployer,
        MockERC20 mockUsdc,
        MockERC20 mockWeth,
        MockERC20 mockWbtc
    )
        internal
        returns (PerpOracle oracle, PerpVault vault, PerpMarket market, PositionManager positionManager)
    {
        oracle = new PerpOracle();
        vault = new PerpVault();
        market = new PerpMarket(address(vault), address(oracle), deployer);
        positionManager = new PositionManager(address(market), address(oracle));

        vault.setMarket(address(market));
        market.setPositionManager(address(positionManager));

        oracle.setManualPrice(address(mockUsdc), 1 * PRICE_PRECISION);
        oracle.setManualPrice(address(mockWeth), 3_450 * PRICE_PRECISION);
        oracle.setManualPrice(address(mockWbtc), 67_000 * PRICE_PRECISION);

        mockUsdc.approve(address(vault), type(uint256).max);
        mockWeth.approve(address(vault), type(uint256).max);
        mockWbtc.approve(address(vault), type(uint256).max);

        vault.addLiquidity(address(mockUsdc), 500_000 ether);
        vault.addLiquidity(address(mockWeth), 500 ether);
        vault.addLiquidity(address(mockWbtc), 25 * 10 ** 8);
    }

    function _logDeployment(Deployment memory deployment) internal pure {
        console.log("Factory:", deployment.factory);
        console.log("Router:", deployment.router);
        console.log("MockUSDC:", deployment.mockUsdc);
        console.log("MockWETH:", deployment.mockWeth);
        console.log("MockWBTC:", deployment.mockWbtc);
        console.log("Primary Pair:", deployment.pair);
        console.log("TokenFactory:", deployment.tokenFactory);
        console.log("StakingToken:", deployment.stakingToken);
        console.log("StakingPool:", deployment.stakingPool);
        console.log("PerpOracle:", deployment.perpOracle);
        console.log("PerpVault:", deployment.perpVault);
        console.log("PerpMarket:", deployment.perpMarket);
        console.log("PositionManager:", deployment.positionManager);
        console.log("OnchainArtworkNFT:", deployment.onchainArtworkNft);
        console.log("IpfsArtworkNFT:", deployment.ipfsArtworkNft);
    }
}

contract DeployLocalScript is DeployUnifiedScript {
    function run() external returns (Deployment memory deployment) {
        // Even local deployment requires an explicit signer. Keeping a known
        // Anvil key in source trains downstream forks to accept committed key
        // material and creates avoidable secret-scanner exceptions.
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address deployer = vm.addr(deployerPrivateKey);
        vm.startBroadcast(deployerPrivateKey);
        deployment = _deployUnified(deployer);
        vm.stopBroadcast();
    }
}

contract DeploySepoliaScript is DeployUnifiedScript {
    function run() external returns (Deployment memory deployment) {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address deployer = vm.addr(deployerPrivateKey);

        vm.startBroadcast(deployerPrivateKey);
        deployment = _deployUnified(deployer);
        vm.stopBroadcast();
    }
}
