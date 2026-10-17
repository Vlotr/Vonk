<?php

namespace App\Http\Controllers;

use InvalidArgumentException;
use RuntimeException;

// Two routes that fail on purpose, for the monitoring tool to show.
class CheckoutController extends Controller
{
    // GET /boom: the same error on every visit, so one issue that counts.
    public function store()
    {
        throw new RuntimeException('Checkout failed');
    }

    // GET /boom-other: a different exception, so an issue of its own.
    public function applyCoupon()
    {
        throw new InvalidArgumentException('Coupon code is not valid');
    }
}
