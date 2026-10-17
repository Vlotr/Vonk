<?php

use App\Http\Controllers\CheckoutController;
use Illuminate\Support\Facades\Route;

Route::get('/', function () {
    return view('welcome');
});

Route::get('/boom', [CheckoutController::class, 'store']);
Route::get('/boom-other', [CheckoutController::class, 'applyCoupon']);
