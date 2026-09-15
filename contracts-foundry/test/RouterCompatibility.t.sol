// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { Test } from "forge-std/Test.sol";
import { TradingPairFactory } from "../src/core/TradingPairFactory.sol";
import { Router } from "../src/periphery/Router.sol";
import { MockERC20 } from "../src/tokens/MockERC20.sol";
import { RouterCompatibility } from "../script/RouterCompatibility.sol";

contract RouterCompatibilityHarness {
    function requireV2Compatible(address router) external view {
        RouterCompatibility.requireV2Compatible(router);
    }
}

abstract contract AddLiquidityEthSurfaceMock {
    // V2 compatibility requires this exact external selector casing.
    // forge-lint: disable-next-line(mixed-case-function)
    function addLiquidityETH(
        address,
        uint256,
        uint256,
        uint256,
        address,
        uint256
    )
        external
        payable
        returns (uint256 amountToken, uint256 amountEth, uint256 liquidity)
    {
        return (0, msg.value, 0);
    }
}

contract CompatibleV2RouterMock is AddLiquidityEthSurfaceMock {
    address public immutable WETH;
    address private immutable _FACTORY;

    constructor(address weth_, address factory_) {
        WETH = weth_;
        _FACTORY = factory_;
    }

    function factory() external view returns (address) {
        return _FACTORY;
    }
}

contract FactoryOnlyV2RouterMock is AddLiquidityEthSurfaceMock {
    function factory() external pure returns (address) {
        return address(0xCAFE);
    }
}

contract WethOnlyV2RouterMock is AddLiquidityEthSurfaceMock {
    address public constant WETH = address(0xBEEF);
}

contract MetadataOnlyRouterMock {
    address public immutable WETH;
    address private immutable _FACTORY;

    constructor(address weth_, address factory_) {
        WETH = weth_;
        _FACTORY = factory_;
    }

    function factory() external view returns (address) {
        return _FACTORY;
    }
}

contract RouterCompatibilityTest is Test {
    RouterCompatibilityHarness internal harness;

    function setUp() external {
        harness = new RouterCompatibilityHarness();
    }

    function testAcceptsCompatibleV2Router() external {
        CompatibleV2RouterMock router = new CompatibleV2RouterMock(address(0xBEEF), address(0xCAFE));

        harness.requireV2Compatible(address(router));
    }

    function testRejectsProjectErc20OnlyRouter() external {
        MockERC20 weth = new MockERC20("Mock WETH", "WETH", 18);
        TradingPairFactory factory = new TradingPairFactory(address(this));
        Router router = new Router(address(factory), address(weth));

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterMissingAddLiquidityETH.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }

    function testRejectsZeroAddress() external {
        vm.expectRevert(RouterCompatibility.RouterAddressZero.selector);
        harness.requireV2Compatible(address(0));
    }

    function testRejectsEoa() external {
        address eoa = address(0xBEEF);

        vm.expectRevert(abi.encodeWithSelector(RouterCompatibility.RouterHasNoCode.selector, eoa));
        harness.requireV2Compatible(eoa);
    }

    function testRejectsRouterWithoutAddLiquidityEthSelector() external {
        MetadataOnlyRouterMock router = new MetadataOnlyRouterMock(address(0xBEEF), address(0xCAFE));

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterMissingAddLiquidityETH.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }

    function testRejectsZeroWethAddress() external {
        CompatibleV2RouterMock router = new CompatibleV2RouterMock(address(0), address(0xCAFE));

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterWethAddressZero.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }

    function testRejectsMissingWethMetadata() external {
        FactoryOnlyV2RouterMock router = new FactoryOnlyV2RouterMock();

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterWethLookupFailed.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }

    function testRejectsZeroFactoryAddress() external {
        CompatibleV2RouterMock router = new CompatibleV2RouterMock(address(0xBEEF), address(0));

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterFactoryAddressZero.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }

    function testRejectsMissingFactoryMetadata() external {
        WethOnlyV2RouterMock router = new WethOnlyV2RouterMock();

        vm.expectRevert(
            abi.encodeWithSelector(
                RouterCompatibility.RouterFactoryLookupFailed.selector, address(router)
            )
        );
        harness.requireV2Compatible(address(router));
    }
}
