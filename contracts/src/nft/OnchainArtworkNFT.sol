// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/token/ERC721/ERC721.sol";
import "@openzeppelin/contracts/utils/Base64.sol";
import "@openzeppelin/contracts/utils/Strings.sol";

contract OnchainArtworkNFT is ERC721, Ownable {
    using Strings for uint256;

    struct Artwork {
        string title;
        string caption;
        string accentColor;
    }

    uint256 private _nextTokenId = 1;
    string public collectionDescription;
    string public canvasColor;

    mapping(uint256 => Artwork) private _artworks;

    constructor(
        string memory name_,
        string memory symbol_,
        string memory collectionDescription_,
        string memory canvasColor_
    ) ERC721(name_, symbol_) Ownable(msg.sender) {
        collectionDescription = collectionDescription_;
        canvasColor = canvasColor_;
    }

    function mintArtwork(
        address to,
        string memory title,
        string memory caption,
        string memory accentColor
    ) external onlyOwner returns (uint256 tokenId) {
        tokenId = _nextTokenId++;
        _artworks[tokenId] = Artwork({
            title: title,
            caption: caption,
            accentColor: bytes(accentColor).length > 0 ? accentColor : "#7dd3fc"
        });
        _safeMint(to, tokenId);
    }

    function artworkOf(uint256 tokenId) external view returns (Artwork memory) {
        _requireOwned(tokenId);
        return _artworks[tokenId];
    }

    function tokenURI(uint256 tokenId) public view override returns (string memory) {
        _requireOwned(tokenId);

        Artwork memory artwork = _artworks[tokenId];
        string memory jsonTitle = _escapeJson(artwork.title);
        string memory jsonCaption = _escapeJson(artwork.caption);
        string memory image = Base64.encode(bytes(_buildSvg(tokenId, artwork)));
        string memory json = Base64.encode(
            bytes(
                string.concat(
                    '{"name":"',
                    jsonTitle,
                    " #",
                    tokenId.toString(),
                    '","description":"',
                    _escapeJson(collectionDescription),
                    " | ",
                    jsonCaption,
                    '","image":"data:image/svg+xml;base64,',
                    image,
                    '","attributes":[{"trait_type":"Caption","value":"',
                    jsonCaption,
                    '"},{"trait_type":"Accent","value":"',
                    _escapeJson(artwork.accentColor),
                    '"},{"trait_type":"Edition","value":"',
                    tokenId.toString(),
                    '"}]}'
                )
            )
        );

        return string.concat("data:application/json;base64,", json);
    }

    function _buildSvg(uint256 tokenId, Artwork memory artwork) private view returns (string memory) {
        string memory title = _escapeSvgText(artwork.title);
        string memory caption = _escapeSvgText(artwork.caption);
        return string.concat(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 800">',
            '<defs><linearGradient id="bg" x1="0%" y1="0%" x2="100%" y2="100%">',
            '<stop offset="0%" stop-color="',
            canvasColor,
            '"/><stop offset="100%" stop-color="#020617"/></linearGradient>',
            '<linearGradient id="accent" x1="0%" y1="0%" x2="100%" y2="0%">',
            '<stop offset="0%" stop-color="',
            artwork.accentColor,
            '"/><stop offset="100%" stop-color="#ffffff"/></linearGradient></defs>',
            '<rect width="800" height="800" fill="url(#bg)"/>',
            '<rect x="48" y="48" width="704" height="704" rx="36" fill="rgba(15,23,42,0.78)" stroke="rgba(255,255,255,0.12)"/>',
            '<circle cx="650" cy="150" r="120" fill="url(#accent)" opacity="0.18"/>',
            '<circle cx="170" cy="620" r="160" fill="url(#accent)" opacity="0.14"/>',
            '<text x="88" y="132" fill="#94a3b8" font-family="Arial" font-size="28">AI Mint Ticket</text>',
            '<text x="88" y="220" fill="#f8fafc" font-family="Arial" font-size="54" font-weight="700">',
            title,
            "</text>",
            '<text x="88" y="298" fill="#cbd5e1" font-family="Arial" font-size="28">',
            caption,
            "</text>",
            '<rect x="88" y="360" width="624" height="4" rx="2" fill="url(#accent)"/>',
            '<text x="88" y="468" fill="#e2e8f0" font-family="Courier New" font-size="140" font-weight="700">#',
            tokenId.toString(),
            "</text>",
            '<text x="88" y="712" fill="',
            artwork.accentColor,
            '" font-family="Arial" font-size="22">On-chain SVG | Metadata stored on-chain</text>',
            "</svg>"
        );
    }

    function _escapeJson(string memory value) private pure returns (string memory) {
        bytes memory source = bytes(value);
        bytes memory buffer = new bytes(source.length * 6);
        uint256 length;

        for (uint256 i = 0; i < source.length; i++) {
            bytes1 char = source[i];
            if (char == "\\") {
                buffer[length++] = "\\";
                buffer[length++] = "\\";
            } else if (char == '"') {
                buffer[length++] = "\\";
                buffer[length++] = '"';
            } else if (char == "\n") {
                buffer[length++] = "\\";
                buffer[length++] = "n";
            } else if (char == "\r") {
                buffer[length++] = "\\";
                buffer[length++] = "r";
            } else if (char == "\t") {
                buffer[length++] = "\\";
                buffer[length++] = "t";
            } else {
                buffer[length++] = char;
            }
        }

        return string(_sliceBytes(buffer, length));
    }

    function _escapeSvgText(string memory value) private pure returns (string memory) {
        bytes memory source = bytes(value);
        bytes memory buffer = new bytes(source.length * 6);
        uint256 length;

        for (uint256 i = 0; i < source.length; i++) {
            bytes1 char = source[i];
            if (char == "&") {
                length = _appendLiteral(buffer, length, "&amp;");
            } else if (char == "<") {
                length = _appendLiteral(buffer, length, "&lt;");
            } else if (char == ">") {
                length = _appendLiteral(buffer, length, "&gt;");
            } else {
                buffer[length++] = char;
            }
        }

        return string(_sliceBytes(buffer, length));
    }

    function _appendLiteral(
        bytes memory buffer,
        uint256 offset,
        string memory value
    ) private pure returns (uint256) {
        bytes memory literal = bytes(value);
        for (uint256 i = 0; i < literal.length; i++) {
            buffer[offset++] = literal[i];
        }

        return offset;
    }

    function _sliceBytes(bytes memory value, uint256 length) private pure returns (bytes memory) {
        bytes memory output = new bytes(length);
        for (uint256 i = 0; i < length; i++) {
            output[i] = value[i];
        }

        return output;
    }
}
