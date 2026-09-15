// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { Test } from "forge-std/Test.sol";
import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { IERC721Errors } from "@openzeppelin/contracts/interfaces/draft-IERC6093.sol";
import { Base64 } from "@openzeppelin/contracts/utils/Base64.sol";
import { IpfsArtworkNFT } from "../src/nft/IpfsArtworkNFT.sol";
import { OnchainArtworkNFT } from "../src/nft/OnchainArtworkNFT.sol";

contract ArtworkNFTTest is Test {
    string internal constant JSON_DATA_PREFIX = "data:application/json;base64,";
    string internal constant SVG_DATA_PREFIX = "data:image/svg+xml;base64,";

    address internal recipient = makeAddr("recipient");
    address internal other = makeAddr("other");

    IpfsArtworkNFT internal ipfsNft;
    OnchainArtworkNFT internal onchainNft;

    event Transfer(address indexed from, address indexed to, uint256 indexed tokenId);

    function setUp() public {
        ipfsNft = new IpfsArtworkNFT("AI Mint Ticket IPFS", "AMTI");
        onchainNft = new OnchainArtworkNFT(
            "AI Mint Ticket", "AMT", "An on-chain SVG NFT for testnet demos.", "#0f172a"
        );
    }

    function test_IpfsMintReturnsIdEmitsTransferAndStoresMetadata() public {
        string memory tokenUri = "ipfs://QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2";

        vm.expectEmit(true, true, true, true, address(ipfsNft));
        emit Transfer(address(0), recipient, 1);
        uint256 tokenId = ipfsNft.mintWithTokenURI(recipient, tokenUri);

        assertEq(tokenId, 1);
        assertEq(ipfsNft.ownerOf(tokenId), recipient);
        assertEq(ipfsNft.balanceOf(recipient), 1);
        assertEq(ipfsNft.tokenURI(tokenId), tokenUri);

        uint256 nextTokenId = ipfsNft.mintWithTokenURI(other, "ipfs://second");
        assertEq(nextTokenId, 2);
        assertEq(ipfsNft.ownerOf(nextTokenId), other);
    }

    function test_IpfsMintRejectsEmptyUriWithoutConsumingTokenId() public {
        vm.expectRevert(bytes("Token URI required"));
        ipfsNft.mintWithTokenURI(recipient, "");

        assertEq(ipfsNft.mintWithTokenURI(recipient, "ipfs://valid"), 1);
    }

    function test_IpfsMintRejectsNonOwner() public {
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, other));
        ipfsNft.mintWithTokenURI(recipient, "ipfs://metadata");
    }

    function testIpfsMetadataReadRejectsUnknownToken() public {
        vm.expectRevert(abi.encodeWithSelector(IERC721Errors.ERC721NonexistentToken.selector, 1));
        ipfsNft.tokenURI(1);
    }

    function test_OnchainMintReturnsIdEmitsTransferAndPersistsArtwork() public {
        vm.expectEmit(true, true, true, true, address(onchainNft));
        emit Transfer(address(0), recipient, 1);
        uint256 tokenId =
            onchainNft.mintArtwork(recipient, "Genesis Ticket", "Minted in test", "#38bdf8");

        assertEq(tokenId, 1);
        assertEq(onchainNft.ownerOf(tokenId), recipient);
        assertEq(onchainNft.balanceOf(recipient), 1);

        OnchainArtworkNFT.Artwork memory artwork = onchainNft.artworkOf(tokenId);
        assertEq(artwork.title, "Genesis Ticket");
        assertEq(artwork.caption, "Minted in test");
        assertEq(artwork.accentColor, "#38bdf8");

        string memory json = _decodeDataUri(onchainNft.tokenURI(tokenId), JSON_DATA_PREFIX);
        assertEq(vm.parseJsonString(json, ".name"), "Genesis Ticket #1");
        assertEq(
            vm.parseJsonString(json, ".description"),
            "An on-chain SVG NFT for testnet demos. | Minted in test"
        );
        assertTrue(_startsWith(vm.parseJsonString(json, ".image"), SVG_DATA_PREFIX));
        assertEq(vm.parseJsonString(json, ".attributes[1].trait_type"), "Accent");
        assertEq(vm.parseJsonString(json, ".attributes[1].value"), "#38bdf8");
    }

    function test_OnchainMintRejectsNonOwner() public {
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, other));
        onchainNft.mintArtwork(recipient, "Unauthorized", "Should fail", "#ef4444");
    }

    function test_OnchainMetadataEscapesJsonAndSvgText() public {
        uint256 tokenId = onchainNft.mintArtwork(
            recipient, 'Genesis "Ticket" <One>', 'Minted & signed by "AI"', "#22c55e"
        );

        string memory json = _decodeDataUri(onchainNft.tokenURI(tokenId), JSON_DATA_PREFIX);
        assertEq(vm.parseJsonString(json, ".name"), 'Genesis "Ticket" <One> #1');
        assertEq(
            vm.parseJsonString(json, ".description"),
            'An on-chain SVG NFT for testnet demos. | Minted & signed by "AI"'
        );

        string memory svg = _decodeDataUri(vm.parseJsonString(json, ".image"), SVG_DATA_PREFIX);
        assertTrue(_contains(svg, 'Genesis "Ticket" &lt;One&gt;'));
        assertTrue(_contains(svg, 'Minted &amp; signed by "AI"'));
    }

    function test_OnchainMetadataEscapesJsonControlCharacters() public {
        string memory title = "Path \\ ticket\n\"quoted\"\r\ttitle";
        string memory caption = "Caption \\ line\n\"quoted\"\r\tvalue";
        uint256 tokenId = onchainNft.mintArtwork(recipient, title, caption, "#22c55e");

        string memory json = _decodeDataUri(onchainNft.tokenURI(tokenId), JSON_DATA_PREFIX);
        assertEq(vm.parseJsonString(json, ".name"), string.concat(title, " #1"));
        assertEq(
            vm.parseJsonString(json, ".description"),
            string.concat("An on-chain SVG NFT for testnet demos. | ", caption)
        );
    }

    function test_OnchainMintUsesDefaultAccentWhenEmpty() public {
        uint256 tokenId = onchainNft.mintArtwork(recipient, "Default", "Accent", "");

        OnchainArtworkNFT.Artwork memory artwork = onchainNft.artworkOf(tokenId);
        assertEq(artwork.accentColor, "#7dd3fc");

        string memory json = _decodeDataUri(onchainNft.tokenURI(tokenId), JSON_DATA_PREFIX);
        assertEq(vm.parseJsonString(json, ".attributes[1].value"), "#7dd3fc");
    }

    function test_OnchainMetadataReadsRejectUnknownToken() public {
        vm.expectRevert(abi.encodeWithSelector(IERC721Errors.ERC721NonexistentToken.selector, 1));
        onchainNft.artworkOf(1);

        vm.expectRevert(abi.encodeWithSelector(IERC721Errors.ERC721NonexistentToken.selector, 1));
        onchainNft.tokenURI(1);
    }

    function _decodeDataUri(
        string memory dataUri,
        string memory prefix
    )
        internal
        pure
        returns (string memory)
    {
        // Assert consumer-visible payloads so malformed JSON or SVG cannot hide behind a valid
        // prefix.
        require(_startsWith(dataUri, prefix), "Unexpected data URI prefix");

        bytes memory source = bytes(dataUri);
        uint256 prefixLength = bytes(prefix).length;
        bytes memory encoded = new bytes(source.length - prefixLength);
        for (uint256 i = 0; i < encoded.length; i++) {
            encoded[i] = source[i + prefixLength];
        }

        return string(Base64.decode(string(encoded)));
    }

    function _startsWith(string memory value, string memory prefix) internal pure returns (bool) {
        bytes memory source = bytes(value);
        bytes memory expected = bytes(prefix);
        if (source.length < expected.length) return false;

        for (uint256 i = 0; i < expected.length; i++) {
            if (source[i] != expected[i]) return false;
        }

        return true;
    }

    function _contains(string memory value, string memory fragment) internal pure returns (bool) {
        bytes memory source = bytes(value);
        bytes memory expected = bytes(fragment);
        if (source.length < expected.length) return false;

        for (uint256 i = 0; i <= source.length - expected.length; i++) {
            bool matches = true;
            for (uint256 j = 0; j < expected.length; j++) {
                if (source[i + j] != expected[j]) {
                    matches = false;
                    break;
                }
            }
            if (matches) return true;
        }

        return false;
    }
}
