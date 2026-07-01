from __future__ import annotations

from copy import deepcopy
from pathlib import Path
from typing import Any

import json


def broad_v2() -> list[dict[str, Any]]:
    return [
        {"symbol": "2558.T", "name": "MAXIS S&P 500 ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2013-01-01", "provider": "yahoo", "provider_symbol": "2558.T", "source_name": "yahoo_market"},
        {"symbol": "1545.T", "name": "NEXT FUNDS NASDAQ-100 ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2010-01-01", "provider": "yahoo", "provider_symbol": "1545.T", "source_name": "yahoo_market"},
        {"symbol": "1328.T", "name": "Nikkei Gold ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "1328.T", "source_name": "yahoo_market"},
        {"symbol": "2510.T", "name": "NEXT FUNDS Japan Bond ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2001-01-01", "provider": "yahoo", "provider_symbol": "2510.T", "source_name": "yahoo_market"},
        {"symbol": "1348.T", "name": "MAXIS NIKKEI 225 ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2011-01-01", "provider": "yahoo", "provider_symbol": "1348.T", "source_name": "yahoo_market"},
        {"symbol": "1475.T", "name": "iShares Core TOPIX ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2014-01-01", "provider": "yahoo", "provider_symbol": "1475.T", "source_name": "yahoo_market"},
        {"symbol": "1489.T", "name": "Nikkei 225 High Dividend Yield Stock 50 ETF", "asset_type": "ETF", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "1489.T", "source_name": "yahoo_market"},
        {"symbol": "7203.T", "name": "Toyota Motor", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "7203.T", "source_name": "yahoo_market"},
        {"symbol": "6758.T", "name": "Sony Group", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "6758.T", "source_name": "yahoo_market"},
        {"symbol": "9984.T", "name": "SoftBank Group", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "9984.T", "source_name": "yahoo_market"},
        {"symbol": "6861.T", "name": "Keyence", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "6861.T", "source_name": "yahoo_market"},
        {"symbol": "8306.T", "name": "Mitsubishi UFJ Financial Group", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "8306.T", "source_name": "yahoo_market"},
        {"symbol": "9432.T", "name": "NTT", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "9432.T", "source_name": "yahoo_market"},
        {"symbol": "6098.T", "name": "Recruit Holdings", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2014-01-01", "provider": "yahoo", "provider_symbol": "6098.T", "source_name": "yahoo_market"},
        {"symbol": "6146.T", "name": "Disco", "asset_type": "STOCK", "venue": "TSE", "currency": "JPY", "timezone": "Asia/Tokyo", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "6146.T", "source_name": "yahoo_market"},
        {"symbol": "BTC-USD", "name": "Bitcoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "BTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ETH-USD", "name": "Ethereum", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ETH-USD", "source_name": "yahoo_market"},
        {"symbol": "SOL-USD", "name": "Solana", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2020-01-01", "provider": "yahoo", "provider_symbol": "SOL-USD", "source_name": "yahoo_market"},
        {"symbol": "XRP-USD", "name": "XRP", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "XRP-USD", "source_name": "yahoo_market"},
        {"symbol": "DOGE-USD", "name": "Dogecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "DOGE-USD", "source_name": "yahoo_market"},
    ]


def broad_v3() -> list[dict[str, Any]]:
    return [
        {"symbol": "VOO", "name": "Vanguard S&P 500 ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2010-01-01", "provider": "yahoo", "provider_symbol": "VOO", "source_name": "yahoo_market"},
        {"symbol": "VT", "name": "Vanguard Total World Stock ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2008-01-01", "provider": "yahoo", "provider_symbol": "VT", "source_name": "yahoo_market"},
        {"symbol": "VTI", "name": "Vanguard Total Stock Market ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2001-01-01", "provider": "yahoo", "provider_symbol": "VTI", "source_name": "yahoo_market"},
        {"symbol": "QQQM", "name": "Invesco NASDAQ 100 ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2020-01-01", "provider": "yahoo", "provider_symbol": "QQQM", "source_name": "yahoo_market"},
        {"symbol": "SPLG", "name": "SPDR Portfolio S&P 500 ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2005-01-01", "provider": "yahoo", "provider_symbol": "SPLG", "source_name": "yahoo_market"},
        {"symbol": "SMH", "name": "VanEck Semiconductor ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2011-01-01", "provider": "yahoo", "provider_symbol": "SMH", "source_name": "yahoo_market"},
        {"symbol": "XBI", "name": "SPDR S&P Biotech ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "XBI", "source_name": "yahoo_market"},
        {"symbol": "XLC", "name": "Communication Services Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2018-01-01", "provider": "yahoo", "provider_symbol": "XLC", "source_name": "yahoo_market"},
        {"symbol": "XLB", "name": "Materials Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLB", "source_name": "yahoo_market"},
        {"symbol": "XLRE", "name": "Real Estate Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2015-01-01", "provider": "yahoo", "provider_symbol": "XLRE", "source_name": "yahoo_market"},
        {"symbol": "XOP", "name": "SPDR S&P Oil & Gas Exploration & Production ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "XOP", "source_name": "yahoo_market"},
        {"symbol": "TSM", "name": "Taiwan Semiconductor", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1997-01-01", "provider": "yahoo", "provider_symbol": "TSM", "source_name": "yahoo_market"},
        {"symbol": "ASML", "name": "ASML Holding", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1995-01-01", "provider": "yahoo", "provider_symbol": "ASML", "source_name": "yahoo_market"},
        {"symbol": "AMD", "name": "Advanced Micro Devices", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "AMD", "source_name": "yahoo_market"},
        {"symbol": "ORCL", "name": "Oracle", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "ORCL", "source_name": "yahoo_market"},
        {"symbol": "COST", "name": "Costco Wholesale", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "COST", "source_name": "yahoo_market"},
        {"symbol": "WMT", "name": "Walmart", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "WMT", "source_name": "yahoo_market"},
        {"symbol": "KO", "name": "Coca-Cola", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "KO", "source_name": "yahoo_market"},
        {"symbol": "DIS", "name": "Walt Disney", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "DIS", "source_name": "yahoo_market"},
        {"symbol": "NVDA", "name": "NVIDIA", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "NVDA", "source_name": "yahoo_market"},
        {"symbol": "AMZN", "name": "Amazon", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "AMZN", "source_name": "yahoo_market"},
        {"symbol": "GOOGL", "name": "Alphabet Class A", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2004-01-01", "provider": "yahoo", "provider_symbol": "GOOGL", "source_name": "yahoo_market"},
        {"symbol": "META", "name": "Meta Platforms", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2012-01-01", "provider": "yahoo", "provider_symbol": "META", "source_name": "yahoo_market"},
        {"symbol": "BTC-USD", "name": "Bitcoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "BTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ETH-USD", "name": "Ethereum", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ETH-USD", "source_name": "yahoo_market"},
        {"symbol": "SOL-USD", "name": "Solana", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2020-01-01", "provider": "yahoo", "provider_symbol": "SOL-USD", "source_name": "yahoo_market"},
        {"symbol": "XRP-USD", "name": "XRP", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "XRP-USD", "source_name": "yahoo_market"},
        {"symbol": "DOGE-USD", "name": "Dogecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "DOGE-USD", "source_name": "yahoo_market"},
        {"symbol": "LTC-USD", "name": "Litecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "LTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ADA-USD", "name": "Cardano", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ADA-USD", "source_name": "yahoo_market"},
    ]


def broad_v4() -> list[dict[str, Any]]:
    return [
        {"symbol": "IEFA", "name": "iShares Core MSCI EAFE ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2012-01-01", "provider": "yahoo", "provider_symbol": "IEFA", "source_name": "yahoo_market"},
        {"symbol": "IEMG", "name": "iShares Core MSCI Emerging Markets ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2012-01-01", "provider": "yahoo", "provider_symbol": "IEMG", "source_name": "yahoo_market"},
        {"symbol": "VEA", "name": "Vanguard FTSE Developed Markets ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "VEA", "source_name": "yahoo_market"},
        {"symbol": "VWO", "name": "Vanguard FTSE Emerging Markets ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2005-01-01", "provider": "yahoo", "provider_symbol": "VWO", "source_name": "yahoo_market"},
        {"symbol": "SCHD", "name": "Schwab U.S. Dividend Equity ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2011-01-01", "provider": "yahoo", "provider_symbol": "SCHD", "source_name": "yahoo_market"},
        {"symbol": "VIG", "name": "Vanguard Dividend Appreciation ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "VIG", "source_name": "yahoo_market"},
        {"symbol": "XLU", "name": "Utilities Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLU", "source_name": "yahoo_market"},
        {"symbol": "XLF", "name": "Financial Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLF", "source_name": "yahoo_market"},
        {"symbol": "XLK", "name": "Technology Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLK", "source_name": "yahoo_market"},
        {"symbol": "XLE", "name": "Energy Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLE", "source_name": "yahoo_market"},
        {"symbol": "XLV", "name": "Health Care Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLV", "source_name": "yahoo_market"},
        {"symbol": "XLI", "name": "Industrial Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLI", "source_name": "yahoo_market"},
        {"symbol": "XLP", "name": "Consumer Staples Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLP", "source_name": "yahoo_market"},
        {"symbol": "XLY", "name": "Consumer Discretionary Select Sector SPDR Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "XLY", "source_name": "yahoo_market"},
        {"symbol": "TQQQ", "name": "ProShares UltraPro QQQ", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2010-01-01", "provider": "yahoo", "provider_symbol": "TQQQ", "source_name": "yahoo_market"},
        {"symbol": "SQQQ", "name": "ProShares UltraPro Short QQQ", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2010-01-01", "provider": "yahoo", "provider_symbol": "SQQQ", "source_name": "yahoo_market"},
        {"symbol": "HYG", "name": "iShares iBoxx $ High Yield Corporate Bond ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "HYG", "source_name": "yahoo_market"},
        {"symbol": "LQD", "name": "iShares iBoxx $ Investment Grade Corporate Bond ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2002-01-01", "provider": "yahoo", "provider_symbol": "LQD", "source_name": "yahoo_market"},
        {"symbol": "TIP", "name": "iShares TIPS Bond ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2003-01-01", "provider": "yahoo", "provider_symbol": "TIP", "source_name": "yahoo_market"},
        {"symbol": "BIL", "name": "SPDR Bloomberg 1-3 Month T-Bill ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "BIL", "source_name": "yahoo_market"},
        {"symbol": "SHV", "name": "iShares Short Treasury Bond ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "SHV", "source_name": "yahoo_market"},
        {"symbol": "MUB", "name": "iShares National Muni Bond ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "MUB", "source_name": "yahoo_market"},
        {"symbol": "VNQ", "name": "Vanguard Real Estate ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2004-01-01", "provider": "yahoo", "provider_symbol": "VNQ", "source_name": "yahoo_market"},
        {"symbol": "VNQI", "name": "Vanguard Global ex-U.S. Real Estate ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2010-01-01", "provider": "yahoo", "provider_symbol": "VNQI", "source_name": "yahoo_market"},
        {"symbol": "GLD", "name": "SPDR Gold Shares", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2004-01-01", "provider": "yahoo", "provider_symbol": "GLD", "source_name": "yahoo_market"},
        {"symbol": "SLV", "name": "iShares Silver Trust", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "SLV", "source_name": "yahoo_market"},
        {"symbol": "USO", "name": "United States Oil Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "USO", "source_name": "yahoo_market"},
        {"symbol": "UNG", "name": "United States Natural Gas Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2007-01-01", "provider": "yahoo", "provider_symbol": "UNG", "source_name": "yahoo_market"},
        {"symbol": "BTC-USD", "name": "Bitcoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "BTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ETH-USD", "name": "Ethereum", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ETH-USD", "source_name": "yahoo_market"},
        {"symbol": "SOL-USD", "name": "Solana", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2020-01-01", "provider": "yahoo", "provider_symbol": "SOL-USD", "source_name": "yahoo_market"},
        {"symbol": "XRP-USD", "name": "XRP", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "XRP-USD", "source_name": "yahoo_market"},
        {"symbol": "DOGE-USD", "name": "Dogecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "DOGE-USD", "source_name": "yahoo_market"},
        {"symbol": "LTC-USD", "name": "Litecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "LTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ADA-USD", "name": "Cardano", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ADA-USD", "source_name": "yahoo_market"},
    ]


def broad_v5() -> list[dict[str, Any]]:
    return [
        {"symbol": "ACWI", "name": "iShares MSCI ACWI ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2008-01-01", "provider": "yahoo", "provider_symbol": "ACWI", "source_name": "yahoo_market"},
        {"symbol": "VYMI", "name": "Vanguard International High Dividend Yield ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2016-01-01", "provider": "yahoo", "provider_symbol": "VYMI", "source_name": "yahoo_market"},
        {"symbol": "VIGI", "name": "Vanguard International Dividend Appreciation ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2016-01-01", "provider": "yahoo", "provider_symbol": "VIGI", "source_name": "yahoo_market"},
        {"symbol": "EWA", "name": "iShares MSCI Australia ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWA", "source_name": "yahoo_market"},
        {"symbol": "EWG", "name": "iShares MSCI Germany ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWG", "source_name": "yahoo_market"},
        {"symbol": "EWJ", "name": "iShares MSCI Japan ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWJ", "source_name": "yahoo_market"},
        {"symbol": "EWZ", "name": "iShares MSCI Brazil ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWZ", "source_name": "yahoo_market"},
        {"symbol": "EWU", "name": "iShares MSCI United Kingdom ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWU", "source_name": "yahoo_market"},
        {"symbol": "EWC", "name": "iShares MSCI Canada ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWC", "source_name": "yahoo_market"},
        {"symbol": "EWH", "name": "iShares MSCI Hong Kong ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWH", "source_name": "yahoo_market"},
        {"symbol": "EWS", "name": "iShares MSCI Singapore ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWS", "source_name": "yahoo_market"},
        {"symbol": "EWT", "name": "iShares MSCI Taiwan ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWT", "source_name": "yahoo_market"},
        {"symbol": "EWY", "name": "iShares MSCI South Korea ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "EWY", "source_name": "yahoo_market"},
        {"symbol": "EPI", "name": "WisdomTree India Earnings Fund", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2008-01-01", "provider": "yahoo", "provider_symbol": "EPI", "source_name": "yahoo_market"},
        {"symbol": "FXI", "name": "iShares China Large-Cap ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2004-01-01", "provider": "yahoo", "provider_symbol": "FXI", "source_name": "yahoo_market"},
        {"symbol": "IYR", "name": "iShares U.S. Real Estate ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "IYR", "source_name": "yahoo_market"},
        {"symbol": "XAR", "name": "SPDR S&P Aerospace & Defense ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2011-01-01", "provider": "yahoo", "provider_symbol": "XAR", "source_name": "yahoo_market"},
        {"symbol": "XRT", "name": "SPDR S&P Retail ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "XRT", "source_name": "yahoo_market"},
        {"symbol": "KRE", "name": "SPDR S&P Regional Banking ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "KRE", "source_name": "yahoo_market"},
        {"symbol": "JETS", "name": "U.S. Global Jets ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2014-01-01", "provider": "yahoo", "provider_symbol": "JETS", "source_name": "yahoo_market"},
        {"symbol": "ITA", "name": "iShares U.S. Aerospace & Defense ETF", "asset_type": "ETF", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "ITA", "source_name": "yahoo_market"},
        {"symbol": "BRK-B", "name": "Berkshire Hathaway B", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1996-01-01", "provider": "yahoo", "provider_symbol": "BRK-B", "source_name": "yahoo_market"},
        {"symbol": "LLY", "name": "Eli Lilly", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "LLY", "source_name": "yahoo_market"},
        {"symbol": "AVGO", "name": "Broadcom", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2009-01-01", "provider": "yahoo", "provider_symbol": "AVGO", "source_name": "yahoo_market"},
        {"symbol": "MRK", "name": "Merck", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "MRK", "source_name": "yahoo_market"},
        {"symbol": "JNJ", "name": "Johnson & Johnson", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "JNJ", "source_name": "yahoo_market"},
        {"symbol": "PG", "name": "Procter & Gamble", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "PG", "source_name": "yahoo_market"},
        {"symbol": "MA", "name": "Mastercard", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2006-01-01", "provider": "yahoo", "provider_symbol": "MA", "source_name": "yahoo_market"},
        {"symbol": "V", "name": "Visa", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2008-01-01", "provider": "yahoo", "provider_symbol": "V", "source_name": "yahoo_market"},
        {"symbol": "NFLX", "name": "Netflix", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2002-01-01", "provider": "yahoo", "provider_symbol": "NFLX", "source_name": "yahoo_market"},
        {"symbol": "PEP", "name": "PepsiCo", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "PEP", "source_name": "yahoo_market"},
        {"symbol": "BABA", "name": "Alibaba", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2014-01-01", "provider": "yahoo", "provider_symbol": "BABA", "source_name": "yahoo_market"},
        {"symbol": "SAP", "name": "SAP", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "1998-01-01", "provider": "yahoo", "provider_symbol": "SAP", "source_name": "yahoo_market"},
        {"symbol": "NVDA", "name": "NVIDIA", "asset_type": "STOCK", "venue": "US", "currency": "USD", "timezone": "America/New_York", "active": 1, "first_date": "2000-01-01", "provider": "yahoo", "provider_symbol": "NVDA", "source_name": "yahoo_market"},
        {"symbol": "BTC-USD", "name": "Bitcoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "BTC-USD", "source_name": "yahoo_market"},
        {"symbol": "ETH-USD", "name": "Ethereum", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "ETH-USD", "source_name": "yahoo_market"},
        {"symbol": "SOL-USD", "name": "Solana", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2020-01-01", "provider": "yahoo", "provider_symbol": "SOL-USD", "source_name": "yahoo_market"},
        {"symbol": "XRP-USD", "name": "XRP", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "XRP-USD", "source_name": "yahoo_market"},
        {"symbol": "DOGE-USD", "name": "Dogecoin", "asset_type": "CRYPTO", "venue": "YAHOO", "currency": "USD", "timezone": "UTC", "active": 1, "first_date": "2017-01-01", "provider": "yahoo", "provider_symbol": "DOGE-USD", "source_name": "yahoo_market"},
    ]


def merge_instruments(existing: list[dict[str, Any]], additions: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], int]:
    merged = [deepcopy(item) for item in existing]
    by_symbol = {item.get("symbol"): idx for idx, item in enumerate(merged) if item.get("symbol")}
    added = 0
    for item in additions:
        symbol = item.get("symbol")
        if not symbol:
            continue
        if symbol in by_symbol:
            continue
        by_symbol[symbol] = len(merged)
        merged.append(deepcopy(item))
        added += 1
    return merged, added


def sync_config(config_path: str | Path, additions: list[dict[str, Any]]) -> int:
    path = Path(config_path)
    config = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {"instruments": []}
    instruments = config.get("instruments", [])
    merged, added = merge_instruments(instruments, additions)
    config["instruments"] = merged
    path.write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return added
